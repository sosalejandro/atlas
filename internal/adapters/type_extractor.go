package adapters

import (
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/sosalejandro/atlas/internal/domain"
)

// TypeExtractor extracts function signatures and struct field information
// from type-checked Go packages. It bridges the gap between the call graph
// (which knows node IDs like "mutationResolver.TrainingLogSet") and the
// go/types system (which knows exact parameter and return types).
type TypeExtractor struct {
	packages []*packages.Package
}

// NewTypeExtractor creates a TypeExtractor from loaded, type-checked packages.
func NewTypeExtractor(pkgs []*packages.Package) *TypeExtractor {
	return &TypeExtractor{packages: pkgs}
}

// FuncContract holds the extracted signature of a function, including its
// parameter and return types with optional struct field expansion.
type FuncContract struct {
	Signature string
	Params    []TypeInfo
	Returns   []TypeInfo
}

// TypeInfo describes a single parameter or return type, optionally expanded
// with struct fields if the underlying type is a struct.
type TypeInfo struct {
	Name     string
	TypeStr  string
	IsStruct bool
	Fields   []domain.ContractField // populated when IsStruct is true
}

// ExtractFuncSignature finds a function by its node ID (ReceiverType.MethodName
// or pkgName.FuncName) and returns its parameter types and return types.
// For each parameter/return that is a struct, it expands the struct fields.
func (e *TypeExtractor) ExtractFuncSignature(nodeID string) (*FuncContract, error) {
	parts := strings.SplitN(nodeID, ".", 2)
	if len(parts) != 2 {
		return nil, nil
	}
	receiverOrPkg := parts[0]
	methodName := parts[1]

	// Search all packages for a matching function.
	for _, pkg := range e.packages {
		if pkg.TypesInfo == nil {
			continue
		}

		for _, obj := range pkg.TypesInfo.Defs {
			if obj == nil {
				continue
			}
			fn, ok := obj.(*types.Func)
			if !ok || fn.Name() != methodName {
				continue
			}

			sig, ok := fn.Type().(*types.Signature)
			if !ok {
				continue
			}

			// Match by receiver type or package name.
			if sig.Recv() != nil {
				recvName := extractRecvTypeName(sig.Recv().Type())
				if recvName != receiverOrPkg {
					continue
				}
			} else {
				// Package-level function — match by package name.
				if fn.Pkg() == nil || fn.Pkg().Name() != receiverOrPkg {
					continue
				}
			}

			// Build the FuncContract.
			fc := &FuncContract{
				Signature: sig.String(),
			}

			// Extract parameter types.
			params := sig.Params()
			for i := 0; i < params.Len(); i++ {
				p := params.At(i)
				ti := e.buildTypeInfo(p.Name(), p.Type())
				fc.Params = append(fc.Params, ti)
			}

			// Extract return types.
			results := sig.Results()
			for i := 0; i < results.Len(); i++ {
				r := results.At(i)
				ti := e.buildTypeInfo(r.Name(), r.Type())
				fc.Returns = append(fc.Returns, ti)
			}

			return fc, nil
		}
	}

	return nil, nil
}

// ExtractStructFields returns all exported fields of a named struct type.
// It searches through loaded packages for a type matching typeName.
func (e *TypeExtractor) ExtractStructFields(typeName string) []domain.ContractField {
	for _, pkg := range e.packages {
		if pkg.Types == nil {
			continue
		}
		obj := pkg.Types.Scope().Lookup(typeName)
		if obj == nil {
			continue
		}
		named, ok := obj.Type().(*types.Named)
		if !ok {
			continue
		}
		st, ok := named.Underlying().(*types.Struct)
		if !ok {
			continue
		}
		return e.extractFields(st)
	}
	return nil
}

// buildTypeInfo constructs a TypeInfo from a types.Type, expanding struct
// fields if the underlying type is a struct.
func (e *TypeExtractor) buildTypeInfo(name string, t types.Type) TypeInfo {
	ti := TypeInfo{
		Name:    name,
		TypeStr: types.TypeString(t, nil),
	}

	// Unwrap pointer to check for struct.
	underlying := t
	if ptr, ok := underlying.(*types.Pointer); ok {
		underlying = ptr.Elem()
	}

	// Check if it's a named type with a struct underlying type.
	if named, ok := underlying.(*types.Named); ok {
		ti.TypeStr = named.Obj().Name()
		if st, ok := named.Underlying().(*types.Struct); ok {
			ti.IsStruct = true
			ti.Fields = e.extractFields(st)
		}
	}

	return ti
}

// ExtractContractTypes implements app.ContractTypeExtractor. It extracts the
// resolved function signature, the first non-context struct input parameter,
// and the first non-error struct return value as domain ContractTypes.
func (e *TypeExtractor) ExtractContractTypes(nodeID string) (signature string, input *domain.ContractType, output *domain.ContractType) {
	fc, err := e.ExtractFuncSignature(nodeID)
	if err != nil || fc == nil {
		return "", nil, nil
	}

	signature = fc.Signature

	// First non-context struct param → input type.
	for _, p := range fc.Params {
		if p.IsStruct && p.TypeStr != "Context" {
			input = &domain.ContractType{
				Name:   p.TypeStr,
				Fields: p.Fields,
			}
			break
		}
	}

	// First struct return → output type.
	for _, r := range fc.Returns {
		if r.IsStruct {
			output = &domain.ContractType{
				Name:   r.TypeStr,
				Fields: r.Fields,
			}
			break
		}
	}

	return signature, input, output
}

// extractFields walks a struct's exported fields and returns ContractField entries.
// Pointer fields are marked as not required (optional); non-pointer fields are required.
func (e *TypeExtractor) extractFields(st *types.Struct) []domain.ContractField {
	var fields []domain.ContractField
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		if !f.Exported() {
			continue
		}

		fieldType := f.Type()
		required := true

		// Pointer fields are optional.
		if _, ok := fieldType.(*types.Pointer); ok {
			required = false
		}

		fields = append(fields, domain.ContractField{
			Name:     f.Name(),
			Type:     types.TypeString(fieldType, nil),
			Required: required,
		})
	}
	return fields
}
