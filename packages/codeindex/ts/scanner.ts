/**
 * Atlas TypeScript AST Scanner
 *
 * Phase 2 — ported from testreg's internal/adapters/ts-scanner.ts and
 * extended with TanStack Router + Expo Router (file-based) support. The
 * Atlas Go orchestrator (packages/codeindex/ts/scanner.go) embeds this
 * file via go:embed, writes it to a tempfile at runtime, and shells out
 * to node.
 *
 * Output contract (JSON, one object on stdout):
 *
 *   {
 *     "nodes": [{ "id", "kind", "file", "line" }, ...],
 *     "edges": [{ "from", "to" }, ...],
 *     "files": [{ "path" }, ...],
 *     "warnings": ["..."],
 *     "stats": { "files_scanned", "routes_found", "api_calls_found" }
 *   }
 *
 * The Go orchestrator maps node.kind onto shared.SymbolKind and emits
 * graph.Edge values verbatim. All file paths are repo-relative
 * (forward-slash) so the persisted shared.FilePosition is portable.
 *
 * Usage:
 *   node --experimental-strip-types scanner.ts --root <project-root>
 *                                              [--include <glob>]...
 *                                              [--exclude <glob>]...
 *                                              [--router react|tanstack|expo]...
 *                                              [--tsconfig <path>]
 *
 * The CLI flags are forwarded by the Go layer. With no --router flags,
 * the scanner auto-detects every router type whose marker files exist
 * inside the project. With no --include/--exclude, the scanner walks
 * known frontend layouts (apps/web*, apps/mobile, src, web, frontend).
 */

import * as ts from 'typescript';
import * as fs from 'node:fs';
import * as path from 'node:path';

// ---------------------------------------------------------------------------
// Types — kept JSON-stable; the Go layer asserts these field names.
// ---------------------------------------------------------------------------

type NodeKind = 'route' | 'component' | 'hook' | 'api-service' | 'endpoint';

interface GraphNode {
  id: string;
  kind: NodeKind;
  file: string;
  line: number;
  doc?: string;
}

interface GraphEdge {
  from: string;
  to: string;
}

interface FileMeta {
  path: string;
}

interface GraphOutput {
  nodes: GraphNode[];
  edges: GraphEdge[];
  files: FileMeta[];
  warnings: string[];
  stats: {
    files_scanned: number;
    routes_found: number;
    api_calls_found: number;
  };
}

interface LazyImportMapping {
  componentName: string;
  modulePath: string;
  exportName: string;
  line: number;
}

interface RouteEntry {
  path: string;
  componentName: string | null;
  line: number;
  children: RouteEntry[];
}

interface ApiMethodInfo {
  objectName: string;
  methodName: string;
  httpMethod: string;
  urlPath: string;
  line: number;
  file: string;
}

interface HookInfo {
  name: string;
  apiCalls: { objectName: string; methodName: string }[];
  hookType: 'query' | 'mutation' | 'unknown';
  line: number;
  file: string;
}

interface ComponentInfo {
  name: string;
  hookCalls: string[];
  line: number;
  file: string;
}

type RouterKind = 'react-router' | 'tanstack' | 'expo';

interface CliArgs {
  root: string;
  include: string[];
  exclude: string[];
  routers: RouterKind[];
  tsconfig: string | null;
}

// ---------------------------------------------------------------------------
// CLI parsing
// ---------------------------------------------------------------------------

function parseArgs(argv: string[]): CliArgs {
  const args: CliArgs = {
    root: '',
    include: [],
    exclude: [],
    routers: [],
    tsconfig: null,
  };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    const next = (): string => {
      const v = argv[++i];
      if (v === undefined) throw new Error(`flag ${a} requires a value`);
      return v;
    };
    switch (a) {
      case '--root':
        args.root = next();
        break;
      case '--include':
        args.include.push(next());
        break;
      case '--exclude':
        args.exclude.push(next());
        break;
      case '--router': {
        const v = next();
        if (v === 'react' || v === 'react-router') args.routers.push('react-router');
        else if (v === 'tanstack') args.routers.push('tanstack');
        else if (v === 'expo') args.routers.push('expo');
        else throw new Error(`unknown router kind: ${v}`);
        break;
      }
      case '--tsconfig':
        args.tsconfig = next();
        break;
      default:
        // Backward-compat: the testreg-era scanner took a positional root.
        if (!args.root && !a.startsWith('--')) {
          args.root = a;
        }
        break;
    }
  }
  if (!args.root) {
    throw new Error('--root is required');
  }
  return args;
}

// ---------------------------------------------------------------------------
// Utility
// ---------------------------------------------------------------------------

function warn(msg: string): void {
  process.stderr.write(`[atlas-ts] WARN: ${msg}\n`);
}

function info(msg: string): void {
  if (process.env.ATLAS_TS_VERBOSE) {
    process.stderr.write(`[atlas-ts] ${msg}\n`);
  }
}

function parseSourceFile(filePath: string): ts.SourceFile {
  const content = fs.readFileSync(filePath, 'utf-8');
  return ts.createSourceFile(
    filePath,
    content,
    ts.ScriptTarget.Latest,
    true,
    filePath.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
  );
}

function getLineNumber(sourceFile: ts.SourceFile, node: ts.Node): number {
  return sourceFile.getLineAndCharacterOfPosition(node.getStart(sourceFile)).line + 1;
}

function toRel(projectRoot: string, absPath: string): string {
  return path.relative(projectRoot, absPath).split(path.sep).join('/');
}

function collectTsFiles(dir: string, extensions: string[], skip: Set<string>): string[] {
  const results: string[] = [];
  if (!fs.existsSync(dir)) return results;
  const entries = fs.readdirSync(dir, { withFileTypes: true });
  for (const entry of entries) {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (skip.has(entry.name) || entry.name.startsWith('.')) continue;
      results.push(...collectTsFiles(fullPath, extensions, skip));
    } else if (entry.isFile() && extensions.some((ext) => entry.name.endsWith(ext))) {
      results.push(fullPath);
    }
  }
  return results;
}

const DEFAULT_SKIP_DIRS = new Set([
  'node_modules',
  '__tests__',
  'tests',
  'test',
  'dist',
  'build',
  'coverage',
  '.next',
  '.expo',
]);

// ---------------------------------------------------------------------------
// Router auto-detection
// ---------------------------------------------------------------------------

function autoDetectRouters(projectRoot: string): RouterKind[] {
  const found = new Set<RouterKind>();
  // Walk a shallow set of likely roots; a real signal is enough.
  const candidates = walkProjectRoots(projectRoot);
  for (const root of candidates) {
    // Expo Router: file-based routes under app/ directory.
    if (fs.existsSync(path.join(root, 'app')) && fs.existsSync(path.join(root, 'package.json'))) {
      const pkg = readPackageJson(path.join(root, 'package.json'));
      if (pkg && (pkg.dependencies?.['expo-router'] || pkg.devDependencies?.['expo-router'])) {
        found.add('expo');
      }
    }
    // TanStack Router: routeTree.gen.ts is the dead-giveaway artifact.
    if (
      fs.existsSync(path.join(root, 'src', 'routeTree.gen.ts')) ||
      fs.existsSync(path.join(root, 'routeTree.gen.ts'))
    ) {
      found.add('tanstack');
    }
    // React Router classic: any of router.tsx, routes.tsx, App.tsx with createBrowserRouter.
    for (const candidate of ['src/router.tsx', 'src/routes.tsx', 'router.tsx', 'routes.tsx']) {
      const p = path.join(root, candidate);
      if (fs.existsSync(p)) {
        const content = fs.readFileSync(p, 'utf-8');
        if (content.includes('createBrowserRouter') || content.includes('createHashRouter')) {
          found.add('react-router');
        } else if (content.includes('createRouter') && content.includes('@tanstack/react-router')) {
          found.add('tanstack');
        }
      }
    }
  }
  return [...found];
}

interface MinPackageJson {
  dependencies?: Record<string, string>;
  devDependencies?: Record<string, string>;
}

function readPackageJson(p: string): MinPackageJson | null {
  try {
    return JSON.parse(fs.readFileSync(p, 'utf-8')) as MinPackageJson;
  } catch {
    return null;
  }
}

// walkProjectRoots returns a small set of likely "frontend project root"
// directories — the project root itself + every direct child of apps/ and
// packages/. We don't recurse further; deeper layouts can be configured
// explicitly with --include.
function walkProjectRoots(projectRoot: string): string[] {
  const out = new Set<string>([projectRoot]);
  for (const sub of ['apps', 'packages']) {
    const dir = path.join(projectRoot, sub);
    if (!fs.existsSync(dir)) continue;
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
      if (entry.isDirectory() && !entry.name.startsWith('.')) {
        out.add(path.join(dir, entry.name));
      }
    }
  }
  return [...out];
}

// ---------------------------------------------------------------------------
// 1. Router Parsing — React Router (createBrowserRouter)
// ---------------------------------------------------------------------------

function extractLazyImports(sourceFile: ts.SourceFile): Map<string, LazyImportMapping> {
  const mappings = new Map<string, LazyImportMapping>();
  ts.forEachChild(sourceFile, function visit(node) {
    if (ts.isVariableStatement(node) && node.declarationList.declarations.length > 0) {
      for (const decl of node.declarationList.declarations) {
        if (!ts.isIdentifier(decl.name) || !decl.initializer) continue;
        const componentName = decl.name.text;
        if (
          ts.isCallExpression(decl.initializer) &&
          ts.isIdentifier(decl.initializer.expression) &&
          decl.initializer.expression.text === 'lazy'
        ) {
          const lazyArg = decl.initializer.arguments[0];
          if (!lazyArg) continue;
          const importPath = extractImportPathFromLazy(lazyArg);
          if (importPath) {
            mappings.set(componentName, {
              componentName,
              modulePath: importPath,
              exportName: componentName,
              line: getLineNumber(sourceFile, node),
            });
          }
        }
      }
    }
    ts.forEachChild(node, visit);
  });
  return mappings;
}

function extractImportPathFromLazy(node: ts.Node): string | null {
  let result: string | null = null;
  function walk(n: ts.Node): void {
    if (result) return;
    if (ts.isCallExpression(n) && n.expression.kind === ts.SyntaxKind.ImportKeyword) {
      const arg = n.arguments[0];
      if (arg && ts.isStringLiteral(arg)) {
        result = arg.text;
        return;
      }
    }
    ts.forEachChild(n, walk);
  }
  walk(node);
  return result;
}

function extractReactRoutes(
  sourceFile: ts.SourceFile,
  lazyImports: Map<string, LazyImportMapping>,
): RouteEntry[] {
  const routes: RouteEntry[] = [];
  ts.forEachChild(sourceFile, function visit(node) {
    if (
      ts.isCallExpression(node) &&
      ts.isIdentifier(node.expression) &&
      (node.expression.text === 'createBrowserRouter' ||
        node.expression.text === 'createHashRouter' ||
        node.expression.text === 'createMemoryRouter')
    ) {
      const arrArg = node.arguments[0];
      if (arrArg && ts.isArrayLiteralExpression(arrArg)) {
        for (const element of arrArg.elements) {
          if (ts.isObjectLiteralExpression(element)) {
            const route = parseReactRouteObject(sourceFile, element, lazyImports);
            if (route) routes.push(route);
          }
        }
      }
    }
    ts.forEachChild(node, visit);
  });
  return routes;
}

function parseReactRouteObject(
  sourceFile: ts.SourceFile,
  obj: ts.ObjectLiteralExpression,
  lazyImports: Map<string, LazyImportMapping>,
): RouteEntry | null {
  let routePath: string | null = null;
  let isIndex = false;
  let componentName: string | null = null;
  const children: RouteEntry[] = [];
  const line = getLineNumber(sourceFile, obj);
  for (const prop of obj.properties) {
    if (!ts.isPropertyAssignment(prop)) continue;
    const propName = prop.name && ts.isIdentifier(prop.name) ? prop.name.text : null;
    if (propName === 'path' && ts.isStringLiteral(prop.initializer)) {
      routePath = prop.initializer.text;
    }
    if (propName === 'index' && prop.initializer.kind === ts.SyntaxKind.TrueKeyword) {
      isIndex = true;
    }
    if (propName === 'element') {
      componentName = extractComponentFromJsx(prop.initializer, lazyImports);
    }
    if (propName === 'children' && ts.isArrayLiteralExpression(prop.initializer)) {
      for (const child of prop.initializer.elements) {
        if (ts.isObjectLiteralExpression(child)) {
          const childRoute = parseReactRouteObject(sourceFile, child, lazyImports);
          if (childRoute) children.push(childRoute);
        }
      }
    }
  }
  if (isIndex) routePath = '';
  if (routePath === null && !isIndex && componentName === null && children.length === 0) {
    return null;
  }
  return { path: routePath ?? '', componentName, line, children };
}

function extractComponentFromJsx(
  node: ts.Node,
  lazyImports: Map<string, LazyImportMapping>,
): string | null {
  if (ts.isJsxSelfClosingElement(node)) {
    const tagName = node.tagName;
    if (ts.isIdentifier(tagName)) {
      const name = tagName.text;
      if (lazyImports.has(name)) return name;
      if (name === 'Navigate' || name === 'Outlet') return null;
      return name;
    }
  }
  if (ts.isJsxElement(node)) {
    for (const child of node.children) {
      const found = extractComponentFromJsx(child, lazyImports);
      if (found) return found;
    }
  }
  if (ts.isParenthesizedExpression(node)) {
    return extractComponentFromJsx(node.expression, lazyImports);
  }
  if (ts.isJsxFragment(node)) {
    for (const child of node.children) {
      const found = extractComponentFromJsx(child, lazyImports);
      if (found) return found;
    }
  }
  let result: string | null = null;
  ts.forEachChild(node, (child) => {
    if (!result) result = extractComponentFromJsx(child, lazyImports);
  });
  return result;
}

function flattenRoutes(
  routes: RouteEntry[],
  parentPath: string,
): { fullPath: string; componentName: string; line: number }[] {
  const results: { fullPath: string; componentName: string; line: number }[] = [];
  for (const route of routes) {
    let currentPath: string;
    if (!route.path) {
      currentPath = parentPath;
    } else if (route.path.startsWith('/')) {
      currentPath = route.path;
    } else {
      currentPath = parentPath === '/' ? `/${route.path}` : `${parentPath}/${route.path}`;
    }
    currentPath = currentPath.replace(/\/+/g, '/');
    if (route.componentName) {
      results.push({ fullPath: currentPath, componentName: route.componentName, line: route.line });
    }
    if (route.children.length > 0) {
      results.push(...flattenRoutes(route.children, currentPath));
    }
  }
  return results;
}

// ---------------------------------------------------------------------------
// 1b. Router Parsing — TanStack Router (createFileRoute / createRoute)
// ---------------------------------------------------------------------------

interface TanstackRoute {
  routePath: string; // e.g. '/dashboard/$id' or '/'
  componentName: string | null;
  file: string; // absolute
  line: number;
}

// TanStack file-based routing convention: routes live under `src/routes/`,
// each file calls `createFileRoute('/path')({ component: Page })`. We parse
// each .ts/.tsx file under that directory.
function extractTanstackRoutes(projectRoot: string, frontendRoot: string): TanstackRoute[] {
  const routes: TanstackRoute[] = [];
  const routesDir = path.join(frontendRoot, 'src', 'routes');
  const altDir = path.join(frontendRoot, 'routes');
  const baseDir = fs.existsSync(routesDir) ? routesDir : fs.existsSync(altDir) ? altDir : null;
  if (!baseDir) return routes;

  const files = collectTsFiles(baseDir, ['.ts', '.tsx'], DEFAULT_SKIP_DIRS).filter(
    (f) => !f.endsWith('routeTree.gen.ts') && !f.endsWith('routeTree.gen.tsx'),
  );

  for (const file of files) {
    const src = parseSourceFile(file);
    ts.forEachChild(src, function visit(node) {
      // const Route = createFileRoute('/path')({ component: Page, ... })
      if (
        ts.isVariableStatement(node) &&
        node.declarationList.declarations.length > 0
      ) {
        for (const decl of node.declarationList.declarations) {
          if (!decl.initializer || !ts.isCallExpression(decl.initializer)) continue;
          const outer = decl.initializer;
          // outer == createFileRoute('/path')(...)
          if (
            ts.isCallExpression(outer.expression) &&
            ts.isIdentifier(outer.expression.expression) &&
            (outer.expression.expression.text === 'createFileRoute' ||
              outer.expression.expression.text === 'createRoute')
          ) {
            let routePath = inferRoutePathFromFile(file, baseDir);
            const firstArg = outer.expression.arguments[0];
            if (firstArg && ts.isStringLiteral(firstArg)) {
              routePath = firstArg.text;
            }
            const cfgArg = outer.arguments[0];
            let componentName: string | null = null;
            if (cfgArg && ts.isObjectLiteralExpression(cfgArg)) {
              for (const prop of cfgArg.properties) {
                if (
                  ts.isPropertyAssignment(prop) &&
                  prop.name &&
                  ts.isIdentifier(prop.name) &&
                  prop.name.text === 'component'
                ) {
                  if (ts.isIdentifier(prop.initializer)) {
                    componentName = prop.initializer.text;
                  }
                }
              }
            }
            routes.push({
              routePath,
              componentName,
              file,
              line: getLineNumber(src, node),
            });
          }
        }
      }
      ts.forEachChild(node, visit);
    });
    void projectRoot; // explicitly unused — kept for symmetry
  }
  return routes;
}

// inferRoutePathFromFile mirrors the TanStack file-based-route convention:
//   src/routes/index.tsx              -> /
//   src/routes/about.tsx              -> /about
//   src/routes/user/$id.tsx           -> /user/$id
//   src/routes/user/$id/edit.tsx      -> /user/$id/edit
function inferRoutePathFromFile(absPath: string, routesDir: string): string {
  let rel = path.relative(routesDir, absPath).split(path.sep).join('/');
  rel = rel.replace(/\.(tsx?|jsx?)$/, '');
  if (rel === 'index' || rel === '__root') return '/';
  rel = rel.replace(/\/index$/, '');
  return '/' + rel;
}

// ---------------------------------------------------------------------------
// 1c. Router Parsing — Expo Router (file-based)
// ---------------------------------------------------------------------------

interface ExpoRoute {
  routePath: string;
  componentName: string | null;
  file: string; // absolute
  line: number;
}

// Expo Router uses Next-style file-based routing under app/. We walk the
// directory and treat each .tsx file as a route; the route path is derived
// from the relative file path. `app/_layout.tsx` and `app/+not-found.tsx`
// are layout/error files; we emit them but mark them as such so they show
// up but don't pollute the route list.
function extractExpoRoutes(projectRoot: string, frontendRoot: string): ExpoRoute[] {
  const routes: ExpoRoute[] = [];
  const appDir = path.join(frontendRoot, 'app');
  if (!fs.existsSync(appDir)) return routes;
  const files = collectTsFiles(appDir, ['.tsx', '.ts', '.jsx', '.js'], DEFAULT_SKIP_DIRS);
  for (const file of files) {
    const basename = path.basename(file);
    if (basename.startsWith('+') && basename !== '+not-found.tsx') continue;
    const routePath = expoFileToRoute(file, appDir);
    const src = parseSourceFile(file);
    const compName = findDefaultExportName(src);
    routes.push({
      routePath,
      componentName: compName,
      file,
      line: 1,
    });
  }
  void projectRoot;
  return routes;
}

function expoFileToRoute(absPath: string, appDir: string): string {
  let rel = path.relative(appDir, absPath).split(path.sep).join('/');
  rel = rel.replace(/\.(tsx?|jsx?)$/, '');
  if (rel === 'index' || rel === '_layout') return '/';
  rel = rel.replace(/\/index$/, '');
  rel = rel.replace(/\/_layout$/, '');
  // Convert [param] → :param and [...rest] → *rest for stable IDs.
  rel = rel.replace(/\[\.\.\.([^\]]+)\]/g, '*$1');
  rel = rel.replace(/\[([^\]]+)\]/g, ':$1');
  return '/' + rel;
}

function findDefaultExportName(src: ts.SourceFile): string | null {
  let found: string | null = null;
  ts.forEachChild(src, (node) => {
    if (found) return;
    // export default function Foo() { ... }
    if (ts.isFunctionDeclaration(node) && node.name) {
      const isDefault = node.modifiers?.some((m) => m.kind === ts.SyntaxKind.DefaultKeyword);
      const isExport = node.modifiers?.some((m) => m.kind === ts.SyntaxKind.ExportKeyword);
      if (isDefault && isExport) {
        found = node.name.text;
        return;
      }
    }
    // export default Foo
    if (ts.isExportAssignment(node) && !node.isExportEquals) {
      if (ts.isIdentifier(node.expression)) {
        found = node.expression.text;
      }
    }
  });
  return found;
}

// ---------------------------------------------------------------------------
// 2. API Service Parsing (ported from testreg ts-scanner)
// ---------------------------------------------------------------------------

function extractFileConstants(sourceFile: ts.SourceFile): Map<string, string> {
  const constants = new Map<string, string>();
  ts.forEachChild(sourceFile, (node) => {
    if (ts.isVariableStatement(node)) {
      for (const decl of node.declarationList.declarations) {
        if (
          ts.isIdentifier(decl.name) &&
          decl.initializer &&
          ts.isStringLiteral(decl.initializer)
        ) {
          constants.set(decl.name.text, decl.initializer.text);
        }
      }
    }
  });
  return constants;
}

function resolveTemplateLiteral(
  node: ts.TemplateLiteral,
  constants: Map<string, string>,
): string {
  if (ts.isNoSubstitutionTemplateLiteral(node)) return node.text;
  if (!ts.isTemplateExpression(node)) return '{unknown}';
  let result = node.head.text;
  for (const span of node.templateSpans) {
    const resolved = resolveExpression(span.expression, constants);
    result += resolved + span.literal.text;
  }
  return result;
}

function resolveExpression(expr: ts.Expression, constants: Map<string, string>): string {
  if (ts.isIdentifier(expr)) {
    return constants.get(expr.text) ?? `{${expr.text}}`;
  }
  if (ts.isPropertyAccessExpression(expr)) {
    if (ts.isIdentifier(expr.expression)) {
      const cv = constants.get(expr.expression.text);
      if (cv) return cv;
    }
    return `{${expr.name.text}}`;
  }
  if (ts.isElementAccessExpression(expr)) return '{param}';
  if (ts.isCallExpression(expr)) {
    if (ts.isPropertyAccessExpression(expr.expression)) {
      if (expr.expression.name.text === 'toString') return '';
    }
    return '{param}';
  }
  if (ts.isBinaryExpression(expr) && expr.operatorToken.kind === ts.SyntaxKind.PlusToken) {
    return resolveExpression(expr.left, constants) + resolveExpression(expr.right, constants);
  }
  if (ts.isStringLiteral(expr)) return expr.text;
  return '{param}';
}

function resolveUrlArg(node: ts.Expression, constants: Map<string, string>): string | null {
  if (ts.isStringLiteral(node)) return node.text;
  if (ts.isTemplateExpression(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
    return resolveTemplateLiteral(node, constants);
  }
  if (ts.isIdentifier(node)) return constants.get(node.text) ?? null;
  return null;
}

function normalizeApiUrl(rawUrl: string): string {
  let url = rawUrl.split('?')[0];
  url = url.replace(/\/+$/, '');
  url = url.replace(/\/+/g, '/');
  if (url.startsWith('/v1/')) url = '/api' + url;
  return url;
}

function getHttpMethod(propName: string): string | null {
  const methods: Record<string, string> = {
    get: 'GET',
    post: 'POST',
    put: 'PUT',
    patch: 'PATCH',
    delete: 'DELETE',
  };
  return methods[propName] ?? null;
}

interface ApiCall {
  httpMethod: string;
  urlPath: string;
  line: number;
}

function collectLocalVariables(
  block: ts.Node,
  fileConstants: Map<string, string>,
): Map<string, string> {
  const locals = new Map<string, string>();
  const combined = new Map(fileConstants);
  function walk(node: ts.Node): void {
    if (ts.isVariableStatement(node) || ts.isVariableDeclarationList(node)) {
      const declList = ts.isVariableStatement(node) ? node.declarationList : node;
      for (const decl of declList.declarations) {
        if (!ts.isIdentifier(decl.name) || !decl.initializer) continue;
        const varName = decl.name.text;
        if (ts.isStringLiteral(decl.initializer)) {
          locals.set(varName, decl.initializer.text);
          combined.set(varName, decl.initializer.text);
        } else if (
          ts.isTemplateExpression(decl.initializer) ||
          ts.isNoSubstitutionTemplateLiteral(decl.initializer)
        ) {
          const resolved = resolveTemplateLiteral(decl.initializer, combined);
          locals.set(varName, resolved);
          combined.set(varName, resolved);
        }
      }
    }
    ts.forEachChild(node, walk);
  }
  walk(block);
  return locals;
}

function findApiClientCalls(
  sourceFile: ts.SourceFile,
  block: ts.Node,
  constants: Map<string, string>,
): ApiCall[] {
  const calls: ApiCall[] = [];
  const localVars = collectLocalVariables(block, constants);
  const allConstants = new Map([...constants, ...localVars]);
  function walk(node: ts.Node): void {
    if (ts.isCallExpression(node) && ts.isPropertyAccessExpression(node.expression)) {
      const propAccess = node.expression;
      const methodName = propAccess.name.text;
      const httpMethod = getHttpMethod(methodName);
      if (httpMethod) {
        const objectName = ts.isIdentifier(propAccess.expression)
          ? propAccess.expression.text
          : null;
        if (objectName === 'apiClient' || objectName === 'api' || objectName === 'client') {
          const urlArg = node.arguments[0];
          if (urlArg) {
            const rawUrl = resolveUrlArg(urlArg, allConstants);
            if (rawUrl) {
              calls.push({
                httpMethod,
                urlPath: normalizeApiUrl(rawUrl),
                line: getLineNumber(sourceFile, node),
              });
            }
          }
        }
      }
    }
    ts.forEachChild(node, walk);
  }
  walk(block);
  return calls;
}

function extractApiServices(
  sourceFile: ts.SourceFile,
  constants: Map<string, string>,
  filePath: string,
  projectRoot: string,
): ApiMethodInfo[] {
  const methods: ApiMethodInfo[] = [];
  const relFile = toRel(projectRoot, filePath);
  ts.forEachChild(sourceFile, function visit(node) {
    if (ts.isVariableStatement(node)) {
      const isExported = node.modifiers?.some((m) => m.kind === ts.SyntaxKind.ExportKeyword);
      if (!isExported) {
        ts.forEachChild(node, visit);
        return;
      }
      for (const decl of node.declarationList.declarations) {
        if (!ts.isIdentifier(decl.name) || !decl.initializer) continue;
        const objName = decl.name.text;
        if (ts.isObjectLiteralExpression(decl.initializer)) {
          extractApiMethodsFromObject(
            sourceFile,
            decl.initializer,
            objName,
            constants,
            relFile,
            methods,
          );
        }
      }
    }
    if (ts.isFunctionDeclaration(node) && node.name) {
      const isExported = node.modifiers?.some((m) => m.kind === ts.SyntaxKind.ExportKeyword);
      if (!isExported) return;
      const funcName = node.name.text;
      if (node.body) {
        const calls = findApiClientCalls(sourceFile, node.body, constants);
        for (const call of calls) {
          methods.push({
            objectName: '',
            methodName: funcName,
            httpMethod: call.httpMethod,
            urlPath: call.urlPath,
            line: call.line,
            file: relFile,
          });
        }
      }
    }
    ts.forEachChild(node, visit);
  });
  return methods;
}

function extractApiMethodsFromObject(
  sourceFile: ts.SourceFile,
  obj: ts.ObjectLiteralExpression,
  objectName: string,
  constants: Map<string, string>,
  relFile: string,
  methods: ApiMethodInfo[],
): void {
  const fileScopeFunctions = new Map<string, ts.Block>();
  ts.forEachChild(sourceFile, (node) => {
    if (ts.isFunctionDeclaration(node) && node.name && node.body) {
      fileScopeFunctions.set(node.name.text, node.body);
    }
    if (ts.isVariableStatement(node)) {
      for (const decl of node.declarationList.declarations) {
        if (
          ts.isIdentifier(decl.name) &&
          decl.initializer &&
          (ts.isArrowFunction(decl.initializer) || ts.isFunctionExpression(decl.initializer))
        ) {
          const body = decl.initializer.body;
          if (ts.isBlock(body)) {
            fileScopeFunctions.set(decl.name.text, body);
          }
        }
      }
    }
  });

  for (const prop of obj.properties) {
    if (ts.isMethodDeclaration(prop) && prop.name && ts.isIdentifier(prop.name)) {
      const methodName = prop.name.text;
      if (prop.body) {
        const calls = findApiClientCalls(sourceFile, prop.body, constants);
        for (const call of calls) {
          methods.push({
            objectName,
            methodName,
            httpMethod: call.httpMethod,
            urlPath: call.urlPath,
            line: getLineNumber(sourceFile, prop),
            file: relFile,
          });
        }
      }
    }
    if (ts.isPropertyAssignment(prop) && prop.name && ts.isIdentifier(prop.name)) {
      const propName = prop.name.text;
      if (
        ts.isFunctionExpression(prop.initializer) ||
        ts.isArrowFunction(prop.initializer)
      ) {
        const body = prop.initializer.body;
        if (ts.isBlock(body)) {
          const calls = findApiClientCalls(sourceFile, body, constants);
          for (const call of calls) {
            methods.push({
              objectName,
              methodName: propName,
              httpMethod: call.httpMethod,
              urlPath: call.urlPath,
              line: getLineNumber(sourceFile, prop),
              file: relFile,
            });
          }
        }
      } else if (ts.isIdentifier(prop.initializer)) {
        const referencedFuncName = prop.initializer.text;
        const funcBody = fileScopeFunctions.get(referencedFuncName);
        if (funcBody) {
          const calls = findApiClientCalls(sourceFile, funcBody, constants);
          for (const call of calls) {
            methods.push({
              objectName,
              methodName: propName,
              httpMethod: call.httpMethod,
              urlPath: call.urlPath,
              line: getLineNumber(sourceFile, prop),
              file: relFile,
            });
          }
        }
      }
    }
    if (ts.isShorthandPropertyAssignment(prop)) {
      const funcName = prop.name.text;
      const funcBody = fileScopeFunctions.get(funcName);
      if (funcBody) {
        const calls = findApiClientCalls(sourceFile, funcBody, constants);
        for (const call of calls) {
          methods.push({
            objectName,
            methodName: funcName,
            httpMethod: call.httpMethod,
            urlPath: call.urlPath,
            line: getLineNumber(sourceFile, prop),
            file: relFile,
          });
        }
      }
    }
  }
}

// ---------------------------------------------------------------------------
// 3. Hook Parsing (ported from testreg ts-scanner)
// ---------------------------------------------------------------------------

function extractImportBindings(
  sourceFile: ts.SourceFile,
): Map<string, { module: string; originalName: string }> {
  const bindings = new Map<string, { module: string; originalName: string }>();
  ts.forEachChild(sourceFile, (node) => {
    if (
      ts.isImportDeclaration(node) &&
      node.moduleSpecifier &&
      ts.isStringLiteral(node.moduleSpecifier)
    ) {
      const modulePath = node.moduleSpecifier.text;
      const importClause = node.importClause;
      if (!importClause) return;
      if (importClause.name) {
        bindings.set(importClause.name.text, { module: modulePath, originalName: 'default' });
      }
      if (importClause.namedBindings && ts.isNamedImports(importClause.namedBindings)) {
        for (const spec of importClause.namedBindings.elements) {
          const localName = spec.name.text;
          const originalName = spec.propertyName ? spec.propertyName.text : localName;
          bindings.set(localName, { module: modulePath, originalName });
        }
      }
    }
  });
  return bindings;
}

function detectHookType(node: ts.FunctionDeclaration): 'query' | 'mutation' | 'unknown' {
  let hookType: 'query' | 'mutation' | 'unknown' = 'unknown';
  function walk(n: ts.Node): void {
    if (ts.isCallExpression(n) && ts.isIdentifier(n.expression)) {
      const name = n.expression.text;
      if (name === 'useQuery' || name === 'useSuspenseQuery') hookType = 'query';
      else if (name === 'useMutation') hookType = 'mutation';
    }
    ts.forEachChild(n, walk);
  }
  if (node.body) walk(node.body);
  return hookType;
}

function findApiServiceCalls(
  node: ts.FunctionDeclaration,
  imports: Map<string, { module: string; originalName: string }>,
): { objectName: string; methodName: string }[] {
  const calls: { objectName: string; methodName: string }[] = [];
  const seen = new Set<string>();
  const apiImports = new Set<string>();
  for (const [name, infoRec] of imports) {
    if (
      infoRec.module.includes('services/api') ||
      infoRec.module.includes('service') ||
      infoRec.module.includes('apiClient') ||
      infoRec.module.includes('lib/api') ||
      infoRec.module.startsWith('@/services') ||
      infoRec.module.startsWith('@/api')
    ) {
      apiImports.add(name);
    }
  }
  function pushCall(objName: string, methodName: string): void {
    const key = `${objName}.${methodName}`;
    if (!seen.has(key)) {
      seen.add(key);
      calls.push({ objectName: objName, methodName });
    }
  }
  function walk(n: ts.Node): void {
    if (
      ts.isCallExpression(n) &&
      ts.isPropertyAccessExpression(n.expression) &&
      ts.isIdentifier(n.expression.expression)
    ) {
      const objName = n.expression.expression.text;
      const methodName = n.expression.name.text;
      if (apiImports.has(objName)) pushCall(objName, methodName);
    }
    ts.forEachChild(n, walk);
  }
  if (node.body) walk(node.body);
  return calls;
}

function extractHooks(
  sourceFile: ts.SourceFile,
  filePath: string,
  projectRoot: string,
): HookInfo[] {
  const hooks: HookInfo[] = [];
  const relFile = toRel(projectRoot, filePath);
  const imports = extractImportBindings(sourceFile);
  ts.forEachChild(sourceFile, function visit(node) {
    if (
      ts.isFunctionDeclaration(node) &&
      node.name &&
      node.name.text.startsWith('use') &&
      node.modifiers?.some((m) => m.kind === ts.SyntaxKind.ExportKeyword)
    ) {
      const hookName = node.name.text;
      const hookType = detectHookType(node);
      const apiCalls = findApiServiceCalls(node, imports);
      hooks.push({
        name: hookName,
        apiCalls,
        hookType,
        line: getLineNumber(sourceFile, node),
        file: relFile,
      });
    }
    ts.forEachChild(node, visit);
  });
  return hooks;
}

// ---------------------------------------------------------------------------
// 4. Page Components — hook-call discovery (used by all router kinds)
// ---------------------------------------------------------------------------

function extractComponents(
  sourceFile: ts.SourceFile,
  filePath: string,
  projectRoot: string,
  allHookNames: Set<string>,
): ComponentInfo[] {
  const components: ComponentInfo[] = [];
  const relFile = toRel(projectRoot, filePath);
  ts.forEachChild(sourceFile, function visit(node) {
    if (
      ts.isFunctionDeclaration(node) &&
      node.name &&
      node.modifiers?.some((m) => m.kind === ts.SyntaxKind.ExportKeyword) &&
      node.body
    ) {
      const compName = node.name.text;
      const hookCalls = findHookCalls(node.body, allHookNames);
      if (hookCalls.length > 0 || isPageOrComponent(compName)) {
        components.push({
          name: compName,
          hookCalls,
          line: getLineNumber(sourceFile, node),
          file: relFile,
        });
      }
    }
    ts.forEachChild(node, visit);
  });
  return components;
}

function isPageOrComponent(name: string): boolean {
  return (
    name.endsWith('Page') ||
    name.endsWith('Tab') ||
    name.endsWith('SubTab') ||
    name.endsWith('Screen') ||
    name.endsWith('Route') ||
    name.endsWith('View') ||
    name.endsWith('Dashboard')
  );
}

function findHookCalls(block: ts.Block, allHookNames: Set<string>): string[] {
  const hooks: string[] = [];
  const seen = new Set<string>();
  function walk(node: ts.Node): void {
    if (ts.isCallExpression(node) && ts.isIdentifier(node.expression)) {
      const name = node.expression.text;
      if (name.startsWith('use') && allHookNames.has(name) && !seen.has(name)) {
        seen.add(name);
        hooks.push(name);
      }
    }
    ts.forEachChild(node, walk);
  }
  walk(block);
  return hooks;
}

// ---------------------------------------------------------------------------
// 5. Component file resolution (used by React Router lazy mappings)
// ---------------------------------------------------------------------------

function resolveComponentFile(
  modulePath: string,
  routerDir: string,
): string | null {
  const resolved = path.resolve(routerDir, modulePath);
  const extensions = ['.tsx', '.ts', '.jsx', '.js'];
  for (const ext of extensions) {
    const candidate = resolved + ext;
    if (fs.existsSync(candidate)) return candidate;
  }
  for (const ext of extensions) {
    const candidate = path.join(resolved, 'index' + ext);
    if (fs.existsSync(candidate)) return candidate;
  }
  return null;
}

// ---------------------------------------------------------------------------
// Main Scanner
// ---------------------------------------------------------------------------

function scan(args: CliArgs): GraphOutput {
  const projectRoot = path.resolve(args.root);
  const nodes = new Map<string, GraphNode>();
  const edges: GraphEdge[] = [];
  const warnings: string[] = [];
  const filesScannedSet = new Set<string>();
  let routesFound = 0;
  let apiCallsFound = 0;

  function addNode(node: GraphNode): void {
    if (!nodes.has(node.id)) nodes.set(node.id, node);
  }
  function addEdge(from: string, to: string): void {
    if (!edges.some((e) => e.from === from && e.to === to)) edges.push({ from, to });
  }

  // Auto-detect routers if none configured.
  let routers = args.routers;
  if (routers.length === 0) {
    routers = autoDetectRouters(projectRoot);
    if (routers.length === 0) {
      // No router signal anywhere — still emit empty result rather than fail.
      info('no router signal detected; emitting empty result');
      return {
        nodes: [],
        edges: [],
        files: [],
        warnings: ['no router signal detected (react-router, tanstack, or expo)'],
        stats: { files_scanned: 0, routes_found: 0, api_calls_found: 0 },
      };
    }
  }

  // Determine frontend roots to walk. If --include provided, use them; else
  // walk the project root + apps/* + packages/* (auto-detect handles which
  // root has which router).
  const frontendRoots =
    args.include.length > 0
      ? args.include.map((p) => path.resolve(projectRoot, p))
      : walkProjectRoots(projectRoot);

  for (const frontendRoot of frontendRoots) {
    if (!fs.existsSync(frontendRoot)) continue;

    // --------------------------------------------------------------------
    // Step 1: Routes (per router kind)
    // --------------------------------------------------------------------
    if (routers.includes('react-router')) {
      processReactRouter(frontendRoot);
    }
    if (routers.includes('tanstack')) {
      processTanstackRouter(frontendRoot);
    }
    if (routers.includes('expo')) {
      processExpoRouter(frontendRoot);
    }

    // --------------------------------------------------------------------
    // Step 2: API services
    // --------------------------------------------------------------------
    const apiServiceDirs = [
      path.join(frontendRoot, 'src', 'services', 'api'),
      path.join(frontendRoot, 'src', 'services'),
      path.join(frontendRoot, 'src', 'api'),
      path.join(frontendRoot, 'src', 'lib', 'api'),
      path.join(frontendRoot, 'services', 'api'),
      path.join(frontendRoot, 'services'),
      path.join(frontendRoot, 'api'),
    ];
    const allApiMethods: ApiMethodInfo[] = [];
    for (const apiDir of apiServiceDirs) {
      if (!fs.existsSync(apiDir)) continue;
      const apiFiles = collectTsFiles(apiDir, ['.ts', '.tsx'], DEFAULT_SKIP_DIRS);
      for (const apiFile of apiFiles) {
        const basename = path.basename(apiFile);
        if (basename === 'client.ts' || basename === 'index.ts') continue;
        const apiSource = parseSourceFile(apiFile);
        filesScannedSet.add(apiFile);
        const constants = extractFileConstants(apiSource);
        const methods = extractApiServices(apiSource, constants, apiFile, projectRoot);
        allApiMethods.push(...methods);
      }
    }
    for (const method of allApiMethods) {
      const serviceId = method.objectName
        ? `${method.objectName}.${method.methodName}`
        : method.methodName;
      addNode({ id: serviceId, kind: 'api-service', file: method.file, line: method.line });
      const endpointId = `${method.httpMethod} ${method.urlPath}`;
      addNode({ id: endpointId, kind: 'endpoint', file: method.file, line: method.line });
      addEdge(serviceId, endpointId);
      apiCallsFound++;
    }

    // --------------------------------------------------------------------
    // Step 3: Hooks
    // --------------------------------------------------------------------
    const hookDirs = [
      path.join(frontendRoot, 'src', 'hooks'),
      path.join(frontendRoot, 'hooks'),
    ];
    const allHooks: HookInfo[] = [];
    for (const hookDir of hookDirs) {
      if (!fs.existsSync(hookDir)) continue;
      const hookFiles = collectTsFiles(hookDir, ['.ts', '.tsx'], DEFAULT_SKIP_DIRS);
      for (const hookFile of hookFiles) {
        const basename = path.basename(hookFile);
        if (basename === 'index.ts' || basename === 'index.tsx') continue;
        const hookSource = parseSourceFile(hookFile);
        filesScannedSet.add(hookFile);
        const hooks = extractHooks(hookSource, hookFile, projectRoot);
        allHooks.push(...hooks);
      }
    }
    const allHookNames = new Set(allHooks.map((h) => h.name));
    allHookNames.add('useAuth');
    allHookNames.add('useDebounce');
    allHookNames.add('useLocalStorage');

    for (const hook of allHooks) {
      addNode({ id: hook.name, kind: 'hook', file: hook.file, line: hook.line });
      for (const apiCall of hook.apiCalls) {
        const serviceId = `${apiCall.objectName}.${apiCall.methodName}`;
        if (nodes.has(serviceId)) {
          addEdge(hook.name, serviceId);
        } else if (nodes.has(apiCall.methodName)) {
          addEdge(hook.name, apiCall.methodName);
        } else {
          warnings.push(
            `Hook ${hook.name} references ${serviceId} but no matching API service was found`,
          );
        }
      }
    }

    // --------------------------------------------------------------------
    // Step 4: Page components → hooks
    // --------------------------------------------------------------------
    const pageDirs = [
      path.join(frontendRoot, 'src', 'pages'),
      path.join(frontendRoot, 'pages'),
      path.join(frontendRoot, 'src', 'routes'),
      path.join(frontendRoot, 'app'), // expo
    ];
    for (const pageDir of pageDirs) {
      if (!fs.existsSync(pageDir)) continue;
      const pageFiles = collectTsFiles(pageDir, ['.tsx', '.ts'], DEFAULT_SKIP_DIRS);
      for (const pageFile of pageFiles) {
        const pageSource = parseSourceFile(pageFile);
        filesScannedSet.add(pageFile);
        const components = extractComponents(pageSource, pageFile, projectRoot, allHookNames);
        for (const comp of components) {
          if (nodes.has(comp.name)) {
            const existing = nodes.get(comp.name);
            if (existing) {
              existing.file = comp.file;
              existing.line = comp.line;
            }
          } else {
            addNode({ id: comp.name, kind: 'component', file: comp.file, line: comp.line });
          }
          for (const hookName of comp.hookCalls) {
            addEdge(comp.name, hookName);
          }
        }
      }
    }
  }

  // -- React Router processing helper (per frontend root) -----------------
  function processReactRouter(frontendRoot: string): void {
    const routerCandidates = [
      path.join(frontendRoot, 'src', 'router.tsx'),
      path.join(frontendRoot, 'src', 'router.ts'),
      path.join(frontendRoot, 'src', 'routes.tsx'),
      path.join(frontendRoot, 'src', 'routes.ts'),
      path.join(frontendRoot, 'src', 'App.tsx'),
      path.join(frontendRoot, 'router.tsx'),
      path.join(frontendRoot, 'routes.tsx'),
    ];
    let routerFile: string | null = null;
    for (const candidate of routerCandidates) {
      if (fs.existsSync(candidate)) {
        routerFile = candidate;
        break;
      }
    }
    if (!routerFile) return;
    const routerSource = parseSourceFile(routerFile);
    filesScannedSet.add(routerFile);
    const lazyImports = extractLazyImports(routerSource);
    const routes = extractReactRoutes(routerSource, lazyImports);
    const flat = flattenRoutes(routes, '/');
    const routerRelFile = toRel(projectRoot, routerFile);
    const routerDir = path.dirname(routerFile);
    for (const route of flat) {
      const routeId = `route:${route.fullPath}`;
      routesFound++;
      addNode({ id: routeId, kind: 'route', file: routerRelFile, line: route.line });
      if (route.componentName) {
        addEdge(routeId, route.componentName);
        const lazyMapping = lazyImports.get(route.componentName);
        if (lazyMapping) {
          const compFile = resolveComponentFile(lazyMapping.modulePath, routerDir);
          if (compFile) {
            addNode({
              id: route.componentName,
              kind: 'component',
              file: toRel(projectRoot, compFile),
              line: 1,
            });
          } else {
            addNode({
              id: route.componentName,
              kind: 'component',
              file: routerRelFile,
              line: lazyMapping.line,
            });
            warnings.push(`react-router: could not resolve file for ${route.componentName}`);
          }
        } else {
          addNode({
            id: route.componentName,
            kind: 'component',
            file: routerRelFile,
            line: route.line,
          });
        }
      }
    }
  }

  // -- TanStack processing helper ----------------------------------------
  function processTanstackRouter(frontendRoot: string): void {
    const routes = extractTanstackRoutes(projectRoot, frontendRoot);
    for (const r of routes) {
      filesScannedSet.add(r.file);
      const routeId = `route:${r.routePath}`;
      routesFound++;
      addNode({
        id: routeId,
        kind: 'route',
        file: toRel(projectRoot, r.file),
        line: r.line,
      });
      // TanStack: the route file itself owns the component; either an
      // explicit `component:` ref or a default export.
      let componentName = r.componentName;
      if (!componentName) {
        const src = parseSourceFile(r.file);
        componentName = findDefaultExportName(src);
      }
      if (componentName) {
        addNode({
          id: componentName,
          kind: 'component',
          file: toRel(projectRoot, r.file),
          line: r.line,
        });
        addEdge(routeId, componentName);
      }
    }
  }

  // -- Expo processing helper --------------------------------------------
  function processExpoRouter(frontendRoot: string): void {
    const routes = extractExpoRoutes(projectRoot, frontendRoot);
    for (const r of routes) {
      filesScannedSet.add(r.file);
      const routeId = `route:${r.routePath}`;
      routesFound++;
      addNode({
        id: routeId,
        kind: 'route',
        file: toRel(projectRoot, r.file),
        line: r.line,
      });
      if (r.componentName) {
        addNode({
          id: r.componentName,
          kind: 'component',
          file: toRel(projectRoot, r.file),
          line: r.line,
        });
        addEdge(routeId, r.componentName);
      }
    }
  }

  const files: FileMeta[] = [];
  for (const abs of filesScannedSet) {
    files.push({ path: toRel(projectRoot, abs) });
  }

  return {
    nodes: [...nodes.values()],
    edges,
    files,
    warnings,
    stats: {
      files_scanned: filesScannedSet.size,
      routes_found: routesFound,
      api_calls_found: apiCallsFound,
    },
  };
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

function main(): void {
  let args: CliArgs;
  try {
    args = parseArgs(process.argv.slice(2));
  } catch (e) {
    process.stderr.write(`[atlas-ts] usage error: ${(e as Error).message}\n`);
    process.exit(2);
  }
  if (!fs.existsSync(args.root)) {
    process.stderr.write(`[atlas-ts] project root not found: ${args.root}\n`);
    process.exit(2);
  }
  const startedAt = Date.now();
  const result = scan(args);
  info(
    `done in ${Date.now() - startedAt}ms — ${result.stats.files_scanned} files, ` +
      `${result.stats.routes_found} routes, ${result.stats.api_calls_found} api calls`,
  );
  process.stdout.write(JSON.stringify(result));
}

main();
void warn;
