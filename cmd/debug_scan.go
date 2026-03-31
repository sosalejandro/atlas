//go:build ignore

package main

import (
    "fmt"
    "go/ast"
    "go/parser"
    "go/token"
)

func main() {
    path := "/home/as-main/Documents/projects/e-commerce-golang/src/pkg/services/products/server/products.grpc.server.go"
    fset := token.NewFileSet()
    file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
    if err != nil {
        fmt.Printf("PARSE ERROR: %v\n", err)
        return
    }
    fmt.Printf("Package: %s\n", file.Name.Name)
    for _, decl := range file.Decls {
        if fn, ok := decl.(*ast.FuncDecl); ok {
            name := fn.Name.Name
            if fn.Recv != nil && len(fn.Recv.List) > 0 {
                if star, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
                    if ident, ok := star.X.(*ast.Ident); ok {
                        name = ident.Name + "." + fn.Name.Name
                    }
                } else if ident, ok := fn.Recv.List[0].Type.(*ast.Ident); ok {
                    name = ident.Name + "." + fn.Name.Name
                }
            }
            fmt.Printf("  %s\n", name)
        }
    }
}
