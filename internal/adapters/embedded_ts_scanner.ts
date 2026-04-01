/**
 * TypeScript AST Scanner for Frontend Dependency Graph
 *
 * Extracts Route -> Page Component -> Hook -> API Service -> API URL
 * from a React/TypeScript project using the TypeScript compiler API.
 *
 * Usage:
 *   node --experimental-strip-types ts-scanner.ts /path/to/project
 *
 * Reads .testreg.yaml from project root for frontend_roots config.
 * Outputs JSON graph to stdout; warnings/errors to stderr.
 */

import * as ts from 'typescript';
import * as fs from 'node:fs';
import * as path from 'node:path';

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface GraphNode {
  id: string;
  kind: 'route' | 'component' | 'hook' | 'api-service' | 'endpoint';
  file: string;
  line: number;
}

interface GraphEdge {
  from: string;
  to: string;
}

interface GraphOutput {
  nodes: GraphNode[];
  edges: GraphEdge[];
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

// ---------------------------------------------------------------------------
// Utility
// ---------------------------------------------------------------------------

function warn(msg: string): void {
  process.stderr.write(`[ts-scanner] WARN: ${msg}\n`);
}

function info(msg: string): void {
  process.stderr.write(`[ts-scanner] ${msg}\n`);
}

function readYamlConfig(projectRoot: string): string[] {
  const configPath = path.join(projectRoot, '.testreg.yaml');
  if (!fs.existsSync(configPath)) {
    warn('.testreg.yaml not found, defaulting to apps/web/src');
    return ['apps/web/src'];
  }

  const content = fs.readFileSync(configPath, 'utf-8');
  const roots: string[] = [];

  // Minimal YAML parsing for frontend_roots list
  const lines = content.split('\n');
  let inFrontendRoots = false;
  for (const line of lines) {
    const trimmed = line.trim();
    if (trimmed === 'frontend_roots:') {
      inFrontendRoots = true;
      continue;
    }
    if (inFrontendRoots) {
      if (trimmed.startsWith('- ')) {
        roots.push(trimmed.slice(2).trim());
      } else if (trimmed && !trimmed.startsWith('#')) {
        // No longer in frontend_roots section
        inFrontendRoots = false;
      }
    }
  }

  if (roots.length === 0) {
    warn('No frontend_roots found in .testreg.yaml, defaulting to apps/web/src');
    return ['apps/web/src'];
  }

  return roots;
}

function parseSourceFile(filePath: string): ts.SourceFile {
  const content = fs.readFileSync(filePath, 'utf-8');
  return ts.createSourceFile(
    filePath,
    content,
    ts.ScriptTarget.Latest,
    true,
    filePath.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS
  );
}

function getLineNumber(sourceFile: ts.SourceFile, node: ts.Node): number {
  return sourceFile.getLineAndCharacterOfPosition(node.getStart(sourceFile)).line + 1;
}

function relativePath(projectRoot: string, absPath: string): string {
  return path.relative(projectRoot, absPath);
}

function collectTsFiles(dir: string, extensions: string[]): string[] {
  const results: string[] = [];
  if (!fs.existsSync(dir)) return results;

  const entries = fs.readdirSync(dir, { withFileTypes: true });
  for (const entry of entries) {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (entry.name === 'node_modules' || entry.name === '__tests__' || entry.name === 'tests') {
        continue;
      }
      results.push(...collectTsFiles(fullPath, extensions));
    } else if (entry.isFile() && extensions.some((ext) => entry.name.endsWith(ext))) {
      results.push(fullPath);
    }
  }
  return results;
}

// ---------------------------------------------------------------------------
// 1. Router Parsing
// ---------------------------------------------------------------------------

/**
 * Extract lazy(() => import(...).then(mod => ({ default: mod.X }))) declarations
 */
function extractLazyImports(sourceFile: ts.SourceFile): Map<string, LazyImportMapping> {
  const mappings = new Map<string, LazyImportMapping>();

  ts.forEachChild(sourceFile, function visit(node) {
    // Looking for: const LoginPage = lazy(...)
    if (
      ts.isVariableStatement(node) &&
      node.declarationList.declarations.length > 0
    ) {
      for (const decl of node.declarationList.declarations) {
        if (!ts.isIdentifier(decl.name) || !decl.initializer) continue;
        const componentName = decl.name.text;

        // Check if initializer is lazy(...)
        if (
          ts.isCallExpression(decl.initializer) &&
          ts.isIdentifier(decl.initializer.expression) &&
          decl.initializer.expression.text === 'lazy'
        ) {
          const lazyArg = decl.initializer.arguments[0];
          if (!lazyArg) continue;

          // Extract module path from the arrow function body: () => import('...').then(...)
          const importPath = extractImportPathFromLazy(lazyArg);
          if (importPath) {
            mappings.set(componentName, {
              componentName,
              modulePath: importPath,
              exportName: componentName, // Named exports typically match
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
  // Walk into the node tree looking for a dynamic import expression
  let result: string | null = null;

  function walk(n: ts.Node): void {
    if (result) return;

    // import('path')
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

/**
 * Extract routes from createBrowserRouter([...]) calls.
 * Handles nested children with path prefix accumulation.
 */
function extractRoutes(
  sourceFile: ts.SourceFile,
  lazyImports: Map<string, LazyImportMapping>
): RouteEntry[] {
  const routes: RouteEntry[] = [];

  ts.forEachChild(sourceFile, function visit(node) {
    // Looking for: createBrowserRouter([...])
    if (
      ts.isCallExpression(node) &&
      ts.isIdentifier(node.expression) &&
      node.expression.text === 'createBrowserRouter'
    ) {
      const arrArg = node.arguments[0];
      if (arrArg && ts.isArrayLiteralExpression(arrArg)) {
        for (const element of arrArg.elements) {
          if (ts.isObjectLiteralExpression(element)) {
            const route = parseRouteObject(sourceFile, element, lazyImports);
            if (route) routes.push(route);
          }
        }
      }
    }
    ts.forEachChild(node, visit);
  });

  return routes;
}

function parseRouteObject(
  sourceFile: ts.SourceFile,
  obj: ts.ObjectLiteralExpression,
  lazyImports: Map<string, LazyImportMapping>
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
          const childRoute = parseRouteObject(sourceFile, child, lazyImports);
          if (childRoute) children.push(childRoute);
        }
      }
    }
  }

  // Determine the path for this entry
  if (isIndex) {
    routePath = '';  // Index route inherits parent path
  }

  if (routePath === null && !isIndex && componentName === null && children.length === 0) {
    return null;  // Nothing useful
  }

  return {
    path: routePath ?? '',
    componentName,
    line,
    children,
  };
}

/**
 * Dig through JSX wrappers (SuspenseRoute, PublicRoute, RoleRoute, ProtectedRoute, etc.)
 * to find the actual page component.
 */
function extractComponentFromJsx(
  node: ts.Node,
  lazyImports: Map<string, LazyImportMapping>
): string | null {
  // Direct JSX: <LoginPage />
  if (ts.isJsxSelfClosingElement(node)) {
    const tagName = node.tagName;
    if (ts.isIdentifier(tagName)) {
      const name = tagName.text;
      if (lazyImports.has(name)) return name;
      // Also accept known wrapper components like Navigate, Outlet
      if (name === 'Navigate' || name === 'Outlet') return null;
      return name;
    }
  }

  // JSX element with children: <Wrapper><LoginPage /></Wrapper>
  if (ts.isJsxElement(node)) {
    // Recursively check children
    for (const child of node.children) {
      const found = extractComponentFromJsx(child, lazyImports);
      if (found) return found;
    }
  }

  // Parenthesized expression: ( <Wrapper>...</Wrapper> )
  if (ts.isParenthesizedExpression(node)) {
    return extractComponentFromJsx(node.expression, lazyImports);
  }

  // JsxFragment
  if (ts.isJsxFragment(node)) {
    for (const child of node.children) {
      const found = extractComponentFromJsx(child, lazyImports);
      if (found) return found;
    }
  }

  // Walk all children as fallback
  let result: string | null = null;
  ts.forEachChild(node, (child) => {
    if (!result) {
      result = extractComponentFromJsx(child, lazyImports);
    }
  });
  return result;
}

/**
 * Flatten route tree into route-path -> component mappings,
 * accumulating path prefixes.
 */
function flattenRoutes(
  routes: RouteEntry[],
  parentPath: string
): { fullPath: string; componentName: string; line: number }[] {
  const results: { fullPath: string; componentName: string; line: number }[] = [];

  for (const route of routes) {
    let currentPath: string;
    if (route.path === '' || route.path === undefined) {
      // Index route or path-less route
      currentPath = parentPath;
    } else if (route.path.startsWith('/')) {
      // Absolute path
      currentPath = route.path;
    } else {
      // Relative path -- join with parent
      currentPath = parentPath === '/' ? `/${route.path}` : `${parentPath}/${route.path}`;
    }

    // Normalize double slashes
    currentPath = currentPath.replace(/\/+/g, '/');

    if (route.componentName) {
      results.push({
        fullPath: currentPath,
        componentName: route.componentName,
        line: route.line,
      });
    }

    if (route.children.length > 0) {
      results.push(...flattenRoutes(route.children, currentPath));
    }
  }

  return results;
}

// ---------------------------------------------------------------------------
// 2. API Service Parsing
// ---------------------------------------------------------------------------

/**
 * Extract file-scope const string declarations for template literal resolution.
 * E.g., const AUTH_BASE = '/v1/auth'
 */
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

/**
 * Resolve a template literal expression into a string path,
 * substituting known constant values. Dynamic params become {param}.
 */
function resolveTemplateLiteral(
  node: ts.TemplateLiteral,
  constants: Map<string, string>
): string {
  if (ts.isNoSubstitutionTemplateLiteral(node)) {
    return node.text;
  }

  if (!ts.isTemplateExpression(node)) {
    return '{unknown}';
  }

  let result = node.head.text;

  for (const span of node.templateSpans) {
    const expr = span.expression;
    const resolved = resolveExpression(expr, constants);
    result += resolved + span.literal.text;
  }

  return result;
}

function resolveExpression(expr: ts.Expression, constants: Map<string, string>): string {
  // Simple identifier: ${AUTH_BASE}
  if (ts.isIdentifier(expr)) {
    return constants.get(expr.text) ?? `{${expr.text}}`;
  }

  // Property access: ${params.id} -> {param}
  if (ts.isPropertyAccessExpression(expr)) {
    // Check if the object is a known constant
    if (ts.isIdentifier(expr.expression)) {
      const constVal = constants.get(expr.expression.text);
      if (constVal) return constVal;
    }
    return `{${expr.name.text}}`;
  }

  // Element access: arr[0]
  if (ts.isElementAccessExpression(expr)) {
    return '{param}';
  }

  // Method call: searchParams.toString() -> ignore (query string)
  if (ts.isCallExpression(expr)) {
    // Check if it's something like searchParams.toString()
    if (ts.isPropertyAccessExpression(expr.expression)) {
      const methodName = expr.expression.name.text;
      if (methodName === 'toString') {
        return '';  // Query strings are not part of the path
      }
    }
    return '{param}';
  }

  // Binary expression: base + '/path'
  if (ts.isBinaryExpression(expr) && expr.operatorToken.kind === ts.SyntaxKind.PlusToken) {
    return resolveExpression(expr.left, constants) + resolveExpression(expr.right, constants);
  }

  // String literal
  if (ts.isStringLiteral(expr)) {
    return expr.text;
  }

  return '{param}';
}

/**
 * Resolve a URL argument to apiClient.get/post/etc.
 * Handles:
 *   - String literals: '/v1/auth/login'
 *   - Template literals: `${AUTH_BASE}/login`
 *   - Identifier references: RECIPES_BASE
 *   - Template with query strings: `${RECIPES_BASE}?${...}`
 */
function resolveUrlArg(node: ts.Expression, constants: Map<string, string>): string | null {
  // String literal
  if (ts.isStringLiteral(node)) {
    return node.text;
  }

  // Template literal
  if (ts.isTemplateExpression(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
    return resolveTemplateLiteral(node, constants);
  }

  // Plain identifier
  if (ts.isIdentifier(node)) {
    return constants.get(node.text) ?? null;
  }

  return null;
}

/**
 * Normalize the API URL path:
 * - Strip query strings
 * - Replace path params with {param}
 * - Prepend /api to /v1 paths
 */
function normalizeApiUrl(rawUrl: string): string {
  // Strip query strings
  let url = rawUrl.split('?')[0];

  // Clean trailing empty segments from template resolution
  url = url.replace(/\/+$/, '');

  // Normalize multiple slashes
  url = url.replace(/\/+/g, '/');

  // Prepend /api to /v1 paths (apiClient has /api as baseURL)
  if (url.startsWith('/v1/')) {
    url = '/api' + url;
  }

  return url;
}

/**
 * Determine HTTP method from apiClient.xxx() call
 */
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

/**
 * Extract API methods from an exported object literal:
 *   export const authApi = { async login(...) { ... apiClient.post(...) ... } }
 *
 * Also handles standalone exported functions that call apiClient directly.
 */
function extractApiServices(
  sourceFile: ts.SourceFile,
  constants: Map<string, string>,
  filePath: string,
  projectRoot: string
): ApiMethodInfo[] {
  const methods: ApiMethodInfo[] = [];
  const relFile = relativePath(projectRoot, filePath);

  ts.forEachChild(sourceFile, function visit(node) {
    // Pattern 1: export const xxxApi = { ... }
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
            sourceFile, decl.initializer, objName, constants, relFile, methods
          );
        }
      }
    }

    // Pattern 2: export async function createCheckoutSession(...) { apiClient.post(...) }
    if (ts.isFunctionDeclaration(node) && node.name) {
      const isExported = node.modifiers?.some((m) => m.kind === ts.SyntaxKind.ExportKeyword);
      if (!isExported) return;

      const funcName = node.name.text;
      // Look for apiClient calls inside the function body
      if (node.body) {
        extractApiCallsFromBlock(sourceFile, node.body, funcName, constants, relFile, methods);
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
  methods: ApiMethodInfo[]
): void {
  // Pre-collect file-scope function bodies for alias resolution
  const fileScopeFunctions = new Map<string, ts.Block>();
  ts.forEachChild(sourceFile, (node) => {
    if (ts.isFunctionDeclaration(node) && node.name && node.body) {
      fileScopeFunctions.set(node.name.text, node.body);
    }
    // Also check variable declarations with arrow/function expressions
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
    // Method declarations: async login(...) { ... }
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

      // PropertyAssignment with a function expression or arrow function
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
      }
      // PropertyAssignment referencing a standalone function: getPricing: fetchPricing
      else if (ts.isIdentifier(prop.initializer)) {
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

    // Shorthand property: { fetchPricing } -- name and value are the same identifier
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

function extractApiCallsFromBlock(
  sourceFile: ts.SourceFile,
  block: ts.Block,
  funcName: string,
  constants: Map<string, string>,
  relFile: string,
  methods: ApiMethodInfo[]
): void {
  const calls = findApiClientCalls(sourceFile, block, constants);
  for (const call of calls) {
    methods.push({
      objectName: '',  // Standalone function
      methodName: funcName,
      httpMethod: call.httpMethod,
      urlPath: call.urlPath,
      line: call.line,
      file: relFile,
    });
  }
}

interface ApiCall {
  httpMethod: string;
  urlPath: string;
  line: number;
}

/**
 * Collect local variable assignments within a block.
 * Handles: const url = `${BASE}/path`, const params = '...', etc.
 * Returns a map of variable name -> resolved string value.
 */
function collectLocalVariables(
  block: ts.Node,
  fileConstants: Map<string, string>
): Map<string, string> {
  const locals = new Map<string, string>();
  // Merge file constants so they're available for local resolution
  const combined = new Map(fileConstants);

  function walk(node: ts.Node): void {
    if (ts.isVariableStatement(node) || ts.isVariableDeclarationList(node)) {
      const declList = ts.isVariableStatement(node) ? node.declarationList : node;
      for (const decl of declList.declarations) {
        if (!ts.isIdentifier(decl.name) || !decl.initializer) continue;
        const varName = decl.name.text;

        // String literal
        if (ts.isStringLiteral(decl.initializer)) {
          locals.set(varName, decl.initializer.text);
          combined.set(varName, decl.initializer.text);
        }
        // Template literal
        else if (
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

/**
 * Find all apiClient.get/post/put/patch/delete calls within a block.
 * Resolves local variable references for URL arguments.
 */
function findApiClientCalls(
  sourceFile: ts.SourceFile,
  block: ts.Node,
  constants: Map<string, string>
): ApiCall[] {
  const calls: ApiCall[] = [];

  // Pre-collect local variables for resolution
  const localVars = collectLocalVariables(block, constants);
  // Merge file-scope constants with local variables for resolution
  const allConstants = new Map([...constants, ...localVars]);

  function walk(node: ts.Node): void {
    if (
      ts.isCallExpression(node) &&
      ts.isPropertyAccessExpression(node.expression)
    ) {
      const propAccess = node.expression;
      const methodName = propAccess.name.text;
      const httpMethod = getHttpMethod(methodName);

      if (httpMethod) {
        // Check if the object is apiClient (or just assume any .get/.post call here)
        const objectName = ts.isIdentifier(propAccess.expression)
          ? propAccess.expression.text
          : null;

        if (objectName === 'apiClient' || objectName === 'api' || objectName === 'client') {
          const urlArg = node.arguments[0];
          if (urlArg) {
            const rawUrl = resolveUrlArg(urlArg, allConstants);
            if (rawUrl) {
              const normalized = normalizeApiUrl(rawUrl);
              calls.push({
                httpMethod,
                urlPath: normalized,
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

// ---------------------------------------------------------------------------
// 3. Hook Parsing
// ---------------------------------------------------------------------------

/**
 * Extract import declarations to map imported names to their source modules.
 * Returns a map: importedName -> { module, originalName }
 */
function extractImportBindings(
  sourceFile: ts.SourceFile
): Map<string, { module: string; originalName: string }> {
  const bindings = new Map<string, { module: string; originalName: string }>();

  ts.forEachChild(sourceFile, (node) => {
    if (ts.isImportDeclaration(node) && node.moduleSpecifier && ts.isStringLiteral(node.moduleSpecifier)) {
      const modulePath = node.moduleSpecifier.text;
      const importClause = node.importClause;
      if (!importClause) return;

      // Default import
      if (importClause.name) {
        bindings.set(importClause.name.text, { module: modulePath, originalName: 'default' });
      }

      // Named imports
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

/**
 * Extract hooks from a hook file.
 * Identifies which API service methods each hook calls.
 */
function extractHooks(
  sourceFile: ts.SourceFile,
  filePath: string,
  projectRoot: string
): HookInfo[] {
  const hooks: HookInfo[] = [];
  const relFile = relativePath(projectRoot, filePath);
  const imports = extractImportBindings(sourceFile);

  ts.forEachChild(sourceFile, function visit(node) {
    // export function useXxx() { ... }
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

/**
 * Detect if a hook is query, mutation, or unknown based on useQuery/useMutation calls.
 */
function detectHookType(node: ts.FunctionDeclaration): 'query' | 'mutation' | 'unknown' {
  let hookType: 'query' | 'mutation' | 'unknown' = 'unknown';

  function walk(n: ts.Node): void {
    if (ts.isCallExpression(n) && ts.isIdentifier(n.expression)) {
      const name = n.expression.text;
      if (name === 'useQuery' || name === 'useSuspenseQuery') {
        hookType = 'query';
      } else if (name === 'useMutation') {
        hookType = 'mutation';
      }
    }
    ts.forEachChild(n, walk);
  }

  if (node.body) walk(node.body);
  return hookType;
}

/**
 * Find API service method calls within a hook function body.
 * Looks for patterns like: mealOptionApi.list(...), authApi.login(...)
 * Also follows through local helper functions defined at file scope.
 */
function findApiServiceCalls(
  node: ts.FunctionDeclaration,
  imports: Map<string, { module: string; originalName: string }>
): { objectName: string; methodName: string }[] {
  const calls: { objectName: string; methodName: string }[] = [];
  const seen = new Set<string>();

  // Identify imported API objects (ones from services/api)
  const apiImports = new Set<string>();
  for (const [name, info] of imports) {
    if (
      info.module.includes('services/api') ||
      info.module.includes('service') ||
      info.module.includes('apiClient') ||
      info.module.includes('lib/api')
    ) {
      apiImports.add(name);
    }
  }

  function walk(n: ts.Node): void {
    // Pattern: xxxApi.method(...)
    if (
      ts.isCallExpression(n) &&
      ts.isPropertyAccessExpression(n.expression) &&
      ts.isIdentifier(n.expression.expression)
    ) {
      const objName = n.expression.expression.text;
      const methodName = n.expression.name.text;

      if (apiImports.has(objName)) {
        const key = `${objName}.${methodName}`;
        if (!seen.has(key)) {
          seen.add(key);
          calls.push({ objectName: objName, methodName });
        }
      }
    }

    // Pattern: standalone function call like fetchMealOptions(...)
    // These are local helpers that call the API -- we trace into them
    if (
      ts.isCallExpression(n) &&
      ts.isIdentifier(n.expression)
    ) {
      const calledFuncName = n.expression.text;
      // Find the local function definition and trace API calls there
      const parent = node.parent;
      if (parent) {
        ts.forEachChild(parent, (sibling) => {
          if (
            ts.isFunctionDeclaration(sibling) &&
            sibling.name?.text === calledFuncName &&
            sibling.body
          ) {
            // Not exported -- a helper function
            const isExported = sibling.modifiers?.some(
              (m) => m.kind === ts.SyntaxKind.ExportKeyword
            );
            if (!isExported) {
              walkForApiCalls(sibling.body);
            }
          }
          // Also check variable declarations with arrow functions
          if (ts.isVariableStatement(sibling)) {
            for (const decl of sibling.declarationList.declarations) {
              if (
                ts.isIdentifier(decl.name) &&
                decl.name.text === calledFuncName &&
                decl.initializer &&
                (ts.isArrowFunction(decl.initializer) || ts.isFunctionExpression(decl.initializer))
              ) {
                const body = decl.initializer.body;
                if (ts.isBlock(body)) {
                  walkForApiCalls(body);
                } else {
                  // Expression body arrow function
                  walkForApiCalls(body);
                }
              }
            }
          }
        });
      }
    }

    ts.forEachChild(n, walk);
  }

  function walkForApiCalls(n: ts.Node): void {
    if (
      ts.isCallExpression(n) &&
      ts.isPropertyAccessExpression(n.expression) &&
      ts.isIdentifier(n.expression.expression)
    ) {
      const objName = n.expression.expression.text;
      const methodName = n.expression.name.text;

      if (apiImports.has(objName)) {
        const key = `${objName}.${methodName}`;
        if (!seen.has(key)) {
          seen.add(key);
          calls.push({ objectName: objName, methodName });
        }
      }
    }
    ts.forEachChild(n, walkForApiCalls);
  }

  if (node.body) walk(node.body);
  return calls;
}

// ---------------------------------------------------------------------------
// 4. Page Component Parsing
// ---------------------------------------------------------------------------

/**
 * Extract page components and identify which hooks they call.
 */
function extractComponents(
  sourceFile: ts.SourceFile,
  filePath: string,
  projectRoot: string,
  allHookNames: Set<string>
): ComponentInfo[] {
  const components: ComponentInfo[] = [];
  const relFile = relativePath(projectRoot, filePath);

  ts.forEachChild(sourceFile, function visit(node) {
    // export function XxxPage() { ... }
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
  return name.endsWith('Page') || name.endsWith('Tab') || name.endsWith('SubTab') || name.endsWith('Dashboard');
}

/**
 * Find hook calls (useXxx) within a function body.
 */
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

    // Also check: const { ... } = useXxx()
    // This is caught by the same pattern above

    ts.forEachChild(node, walk);
  }

  walk(block);
  return hooks;
}

// ---------------------------------------------------------------------------
// 5. Resolve component file paths from lazy imports
// ---------------------------------------------------------------------------

function resolveComponentFile(
  modulePath: string,
  routerDir: string,
  projectRoot: string
): string | null {
  // modulePath is relative to the router file, e.g., './pages/auth/LoginPage'
  const resolved = path.resolve(routerDir, modulePath);

  // Try extensions
  const extensions = ['.tsx', '.ts', '.jsx', '.js'];
  for (const ext of extensions) {
    const candidate = resolved + ext;
    if (fs.existsSync(candidate)) {
      return candidate;
    }
  }

  // Try index file
  for (const ext of extensions) {
    const candidate = path.join(resolved, 'index' + ext);
    if (fs.existsSync(candidate)) {
      return candidate;
    }
  }

  return null;
}

// ---------------------------------------------------------------------------
// Main Scanner
// ---------------------------------------------------------------------------

function scan(projectRoot: string): GraphOutput {
  const nodes = new Map<string, GraphNode>();
  const edges: GraphEdge[] = [];
  const warnings: string[] = [];
  let filesScanned = 0;
  let routesFound = 0;
  let apiCallsFound = 0;

  const frontendRoots = readYamlConfig(projectRoot);

  function addNode(node: GraphNode): void {
    if (!nodes.has(node.id)) {
      nodes.set(node.id, node);
    }
  }

  function addEdge(from: string, to: string): void {
    // Avoid duplicate edges
    if (!edges.some((e) => e.from === from && e.to === to)) {
      edges.push({ from, to });
    }
  }

  for (const root of frontendRoots) {
    const rootAbsPath = path.resolve(projectRoot, root);
    if (!fs.existsSync(rootAbsPath)) {
      warnings.push(`Frontend root not found: ${root}`);
      continue;
    }

    info(`Scanning frontend root: ${root}`);

    // -----------------------------------------------------------------------
    // Step 1: Find and parse router file
    // -----------------------------------------------------------------------
    const routerCandidates = [
      path.join(rootAbsPath, 'router.tsx'),
      path.join(rootAbsPath, 'router.ts'),
      path.join(rootAbsPath, 'routes.tsx'),
      path.join(rootAbsPath, 'routes.ts'),
      path.join(rootAbsPath, 'App.tsx'),
    ];

    let routerFile: string | null = null;
    for (const candidate of routerCandidates) {
      if (fs.existsSync(candidate)) {
        routerFile = candidate;
        break;
      }
    }

    if (routerFile) {
      info(`Parsing router: ${relativePath(projectRoot, routerFile)}`);
      const routerSource = parseSourceFile(routerFile);
      filesScanned++;

      const lazyImports = extractLazyImports(routerSource);
      const routes = extractRoutes(routerSource, lazyImports);
      const flatRoutes = flattenRoutes(routes, '/');

      const routerRelFile = relativePath(projectRoot, routerFile);
      const routerDir = path.dirname(routerFile);

      for (const route of flatRoutes) {
        const routeId = `route:${route.fullPath}`;
        routesFound++;

        addNode({
          id: routeId,
          kind: 'route',
          file: routerRelFile,
          line: route.line,
        });

        // Link route to component
        if (route.componentName) {
          addEdge(routeId, route.componentName);
        }

        // Resolve lazy import to file path for component node
        const lazyMapping = lazyImports.get(route.componentName);
        if (lazyMapping) {
          const compFile = resolveComponentFile(lazyMapping.modulePath, routerDir, projectRoot);
          if (compFile) {
            addNode({
              id: route.componentName,
              kind: 'component',
              file: relativePath(projectRoot, compFile),
              line: 1,
            });
          } else {
            addNode({
              id: route.componentName,
              kind: 'component',
              file: routerRelFile,
              line: lazyMapping.line,
            });
            warnings.push(`Could not resolve file for component: ${route.componentName}`);
          }
        }
      }
    } else {
      warnings.push(`No router file found in ${root}`);
    }

    // -----------------------------------------------------------------------
    // Step 2: Parse API service files
    // -----------------------------------------------------------------------
    const apiServiceDirs = [
      path.join(rootAbsPath, 'services', 'api'),
      path.join(rootAbsPath, 'services'),
      path.join(rootAbsPath, 'api'),
    ];

    const allApiMethods: ApiMethodInfo[] = [];

    for (const apiDir of apiServiceDirs) {
      if (!fs.existsSync(apiDir)) continue;

      const apiFiles = collectTsFiles(apiDir, ['.ts', '.tsx']);
      for (const apiFile of apiFiles) {
        // Skip client.ts, index.ts, and type-only files
        const basename = path.basename(apiFile);
        if (basename === 'client.ts' || basename === 'index.ts') continue;

        const apiSource = parseSourceFile(apiFile);
        filesScanned++;
        const constants = extractFileConstants(apiSource);
        const methods = extractApiServices(apiSource, constants, apiFile, projectRoot);
        allApiMethods.push(...methods);
      }
    }

    // Create nodes for API methods and endpoint nodes
    for (const method of allApiMethods) {
      const serviceId = method.objectName
        ? `${method.objectName}.${method.methodName}`
        : method.methodName;

      addNode({
        id: serviceId,
        kind: 'api-service',
        file: method.file,
        line: method.line,
      });

      const endpointId = `${method.httpMethod} ${method.urlPath}`;
      addNode({
        id: endpointId,
        kind: 'endpoint',
        file: method.file,
        line: method.line,
      });

      addEdge(serviceId, endpointId);
      apiCallsFound++;
    }

    // -----------------------------------------------------------------------
    // Step 3: Parse hook files
    // -----------------------------------------------------------------------
    const hookDirs = [
      path.join(rootAbsPath, 'hooks'),
    ];

    const allHooks: HookInfo[] = [];

    for (const hookDir of hookDirs) {
      if (!fs.existsSync(hookDir)) continue;

      const hookFiles = collectTsFiles(hookDir, ['.ts', '.tsx']);
      for (const hookFile of hookFiles) {
        const basename = path.basename(hookFile);
        if (basename === 'index.ts' || basename === 'index.tsx') continue;

        const hookSource = parseSourceFile(hookFile);
        filesScanned++;
        const hooks = extractHooks(hookSource, hookFile, projectRoot);
        allHooks.push(...hooks);
      }
    }

    // Create hook nodes and edges to API services
    const allHookNames = new Set(allHooks.map((h) => h.name));
    // Also add well-known hooks not in our scanning scope
    allHookNames.add('useAuth');
    allHookNames.add('useDebounce');
    allHookNames.add('useLocalStorage');

    for (const hook of allHooks) {
      addNode({
        id: hook.name,
        kind: 'hook',
        file: hook.file,
        line: hook.line,
      });

      for (const apiCall of hook.apiCalls) {
        const serviceId = `${apiCall.objectName}.${apiCall.methodName}`;
        // Only link if we have a matching API service node
        if (nodes.has(serviceId)) {
          addEdge(hook.name, serviceId);
        } else {
          // Try without object name for standalone functions
          const standaloneId = apiCall.methodName;
          if (nodes.has(standaloneId)) {
            addEdge(hook.name, standaloneId);
          } else {
            warnings.push(
              `Hook ${hook.name} references ${serviceId} but no matching API service was found`
            );
          }
        }
      }
    }

    // -----------------------------------------------------------------------
    // Step 4: Parse page components for hook usage
    // -----------------------------------------------------------------------
    const pageDir = path.join(rootAbsPath, 'pages');
    if (fs.existsSync(pageDir)) {
      const pageFiles = collectTsFiles(pageDir, ['.tsx', '.ts']);
      for (const pageFile of pageFiles) {
        const pageSource = parseSourceFile(pageFile);
        filesScanned++;
        const components = extractComponents(pageSource, pageFile, projectRoot, allHookNames);

        for (const comp of components) {
          // Update component node file/line if we parsed it
          if (nodes.has(comp.name)) {
            const existing = nodes.get(comp.name)!;
            existing.file = comp.file;
            existing.line = comp.line;
          } else {
            addNode({
              id: comp.name,
              kind: 'component',
              file: comp.file,
              line: comp.line,
            });
          }

          // Link component to hooks
          for (const hookName of comp.hookCalls) {
            addEdge(comp.name, hookName);
          }
        }
      }
    }
  }

  return {
    nodes: Array.from(nodes.values()),
    edges,
    warnings,
    stats: {
      files_scanned: filesScanned,
      routes_found: routesFound,
      api_calls_found: apiCallsFound,
    },
  };
}

// ---------------------------------------------------------------------------
// Entry Point
// ---------------------------------------------------------------------------

function main(): void {
  const args = process.argv.slice(2);
  if (args.length === 0) {
    process.stderr.write('Usage: node --experimental-strip-types ts-scanner.ts <project-root>\n');
    process.exit(1);
  }

  const projectRoot = path.resolve(args[0]);
  if (!fs.existsSync(projectRoot)) {
    process.stderr.write(`Error: project root not found: ${projectRoot}\n`);
    process.exit(1);
  }

  info(`Scanning project: ${projectRoot}`);
  const startTime = Date.now();

  const result = scan(projectRoot);

  const elapsed = Date.now() - startTime;
  info(
    `Done in ${elapsed}ms: ${result.stats.files_scanned} files, ` +
    `${result.stats.routes_found} routes, ${result.stats.api_calls_found} API calls`
  );

  if (result.warnings.length > 0) {
    info(`${result.warnings.length} warnings (see output)`);
  }

  process.stdout.write(JSON.stringify(result, null, 2) + '\n');
}

main();
