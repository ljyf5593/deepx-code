package codegraph

import (
	"context"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"

	"golang.org/x/tools/go/packages"
)

// goPreciseCallEdges 用 go/packages + go/types 对 workspace 里的 Go 代码做"语义级"调用解析,
// 把方法调用精确到接收者类型(如 "User.Save"),消除"同名合并"。
//
// 第二个返回值 ok=false 表示放弃精确(没装 Go 工具链 / 不是合法 module / 有编译错误),
// 调用方应退回语法层近似——绝不因为代码不可编译而漏掉调用边。
func goPreciseCallEdges(root string) ([]Edge, bool) {
	fset := token.NewFileSet()
	// 超时兜底(issue #115):体量门控已在上游(maybePrecise)拦掉过大项目,这里再加一道
	// 时间上限,防止 go list / 类型检查在病态依赖下长时间挂住。
	loadCtx, cancel := context.WithTimeout(context.Background(), maxIndexDuration)
	defer cancel()
	cfg := &packages.Config{
		Context: loadCtx,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
		Dir:  root,
		Fset: fset,
		// 只读:绝不让 go list 修改用户的 go.mod/go.sum(覆盖用户可能设的 GOFLAGS=-mod=mod);
		// 依赖缺失时它会报错而非改文件 → 我们 hadErr 分支回退语法层,安全降级。
		BuildFlags: []string{"-mod=readonly"},
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil || len(pkgs) == 0 {
		return nil, false
	}
	// 任何加载 / 类型错误都放弃精确(类型信息可能不全 → 会漏边),退回语法近似保证完整。
	hadErr := false
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if len(p.Errors) > 0 {
			hadErr = true
		}
	})
	if hadErr {
		return nil, false
	}

	var edges []Edge
	edges = append(edges, goTypeHierarchy(pkgs, fset, root)...) // 继承/嵌入 + 接口实现边
	for _, pkg := range pkgs {
		info := pkg.TypesInfo
		if info == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			rel := toRel(root, fset.Position(file.Pos()).Filename)
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				from := fd.Name.Name
				if fn, ok := info.Defs[fd.Name].(*types.Func); ok {
					_, from = goFuncNames(fn)
				}
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					ce, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if short, qual, ok := resolveGoCallee(info, ce.Fun); ok {
						edges = append(edges, Edge{
							From: from, To: short, ToQual: qual, Kind: EdgeCall,
							File: rel, Line: fset.Position(ce.Pos()).Line,
						})
					}
					return true
				})
			}
		}
	}
	return edges, true
}

// resolveGoCallee 把调用表达式的 Fun 解析成被调用函数的短名 + 限定名。
//
//	foo()      → ("foo","foo")
//	pkg.Foo()  → ("Foo","Foo")            (跨包函数,我们不带包前缀,跟符号命名一致)
//	x.Save()   → ("Save","User.Save")     (方法,精确到接收者类型)
func resolveGoCallee(info *types.Info, fun ast.Expr) (short, qual string, ok bool) {
	switch e := fun.(type) {
	case *ast.Ident:
		if fn, ok := info.Uses[e].(*types.Func); ok {
			s, q := goFuncNames(fn)
			return s, q, true
		}
	case *ast.SelectorExpr:
		if sel, ok := info.Selections[e]; ok {
			if fn, ok := sel.Obj().(*types.Func); ok {
				s, q := goFuncNames(fn)
				return s, q, true
			}
		}
		if fn, ok := info.Uses[e.Sel].(*types.Func); ok {
			s, q := goFuncNames(fn)
			return s, q, true
		}
	}
	return "", "", false
}

// goTypeHierarchy 从类型信息抽继承/嵌入边(struct/interface 嵌入)和接口实现边。
// File/Line 指向"子类型/实现类型"的定义位置。只在工作区包内的类型/接口间计算。
func goTypeHierarchy(pkgs []*packages.Package, fset *token.FileSet, root string) []Edge {
	var named []*types.Named
	for _, pkg := range pkgs {
		if pkg.Types == nil {
			continue
		}
		scope := pkg.Types.Scope()
		for _, n := range scope.Names() {
			if tn, ok := scope.Lookup(n).(*types.TypeName); ok {
				if nt, ok := tn.Type().(*types.Named); ok {
					named = append(named, nt)
				}
			}
		}
	}

	loc := func(nt *types.Named) (string, int) {
		p := fset.Position(nt.Obj().Pos())
		return toRel(root, p.Filename), p.Line
	}

	var edges []Edge
	var ifaces, concretes []*types.Named
	for _, nt := range named {
		f, l := loc(nt)
		switch u := nt.Underlying().(type) {
		case *types.Struct:
			concretes = append(concretes, nt)
			for i := 0; i < u.NumFields(); i++ {
				if fld := u.Field(i); fld.Embedded() {
					if base := namedBaseName(fld.Type()); base != "" {
						edges = append(edges, Edge{From: nt.Obj().Name(), To: base, Kind: EdgeExtends, File: f, Line: l})
					}
				}
			}
		case *types.Interface:
			ifaces = append(ifaces, nt)
			for i := 0; i < u.NumEmbeddeds(); i++ {
				if base := namedBaseName(u.EmbeddedType(i)); base != "" {
					edges = append(edges, Edge{From: nt.Obj().Name(), To: base, Kind: EdgeExtends, File: f, Line: l})
				}
			}
		default:
			concretes = append(concretes, nt) // 非接口类型(如带方法的别名)也可能实现接口
		}
	}

	// 接口实现:每个具体类型 × 每个工作区接口,types.Implements 判定(值或指针)。
	for _, T := range concretes {
		f, l := loc(T)
		for _, I := range ifaces {
			iface, _ := I.Underlying().(*types.Interface)
			if iface == nil || iface.NumMethods() == 0 || T.Obj().Name() == I.Obj().Name() {
				continue // 跳过空接口(any,谁都"实现",纯噪音)
			}
			if types.Implements(T, iface) || types.Implements(types.NewPointer(T), iface) {
				edges = append(edges, Edge{From: T.Obj().Name(), To: I.Obj().Name(), Kind: EdgeImplements, File: f, Line: l})
			}
		}
	}
	return edges
}

// namedBaseName 剥指针取裸命名类型名。
func namedBaseName(t types.Type) string {
	switch x := t.(type) {
	case *types.Pointer:
		return namedBaseName(x.Elem())
	case *types.Named:
		return x.Obj().Name()
	}
	return ""
}

// goFuncNames 返回 *types.Func 的短名与限定名:方法 → "接收者类型.方法",函数 → 短名。
func goFuncNames(fn *types.Func) (short, qual string) {
	short = fn.Name()
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		if base := recvBaseName(sig.Recv().Type()); base != "" {
			return short, base + "." + short
		}
	}
	return short, short
}

// recvBaseName 剥掉指针 / 泛型实参,取接收者的裸类型名。
func recvBaseName(t types.Type) string {
	switch x := t.(type) {
	case *types.Pointer:
		return recvBaseName(x.Elem())
	case *types.Named:
		return x.Obj().Name()
	}
	return ""
}

func toRel(root, abs string) string {
	if rel, err := filepath.Rel(root, abs); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(abs)
}
