package agentd

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The guard scans of DESIGN-025 section 11 for H4. Each reads syntax, not
// types, so each has a testdata directory, never compiled, whose functions
// each break one rule; the scan must name every one of them, or the real
// scan passing proves less than it claims. Review still covers what syntax
// cannot show: a value that reaches a call through another package, a
// method call on a file opened elsewhere, and reflection.

// parsedFile is one parsed source file and its import names.
type parsedFile struct {
	name    string
	file    *ast.File
	imports map[string]bool
}

func parseGoFiles(t *testing.T, fset *token.FileSet, paths []string) []parsedFile {
	t.Helper()
	var out []parsedFile
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		pf := parsedFile{name: p, file: f, imports: map[string]bool{}}
		for _, im := range f.Imports {
			ip, _ := strconv.Unquote(im.Path.Value)
			n := path.Base(ip)
			if im.Name != nil {
				n = im.Name.Name
			}
			pf.imports[n] = true
		}
		out = append(out, pf)
	}
	if len(out) == 0 {
		t.Fatalf("no Go files in %v", paths)
	}
	return out
}

func globGo(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// eachFunc calls visit for every function body: declared functions and
// methods by name, and function literals in package-level values under the
// declared name.
func eachFunc(files []parsedFile, visit func(pf parsedFile, fn string, decl *ast.FuncDecl, body *ast.BlockStmt)) {
	for _, pf := range files {
		for _, d := range pf.file.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Body != nil {
					visit(pf, d.Name.Name, d, d.Body)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok || len(vs.Names) == 0 {
						continue
					}
					for _, v := range vs.Values {
						ast.Inspect(v, func(n ast.Node) bool {
							if lit, ok := n.(*ast.FuncLit); ok {
								visit(pf, vs.Names[0].Name, nil, lit.Body)
								return false
							}
							return true
						})
					}
				}
			}
		}
	}
}

// pkgCall returns pkg and name for a call pkg.Name(...) where pkg is an
// imported package.
func pkgCall(pf parsedFile, call *ast.CallExpr) (string, string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok || !pf.imports[x.Name] {
		return "", "", false
	}
	return x.Name, sel.Sel.Name, true
}

// definedFuncs are the names of the functions, methods and package-level
// variables the files declare: a variable's function literal is scanned under
// its name.
func definedFuncs(files []parsedFile) map[string]bool {
	out := map[string]bool{}
	for _, pf := range files {
		for _, d := range pf.file.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				out[d.Name.Name] = true
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, n := range vs.Names {
							out[n.Name] = true
						}
					}
				}
			}
		}
	}
	return out
}

// localNames are the names a body declares: a closure it calls is scanned
// as part of the body, and a function value it holds came from a scanned call.
func localNames(body *ast.BlockStmt) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE {
				for _, l := range n.Lhs {
					if id, ok := l.(*ast.Ident); ok {
						out[id.Name] = true
					}
				}
			}
		case *ast.ValueSpec:
			for _, id := range n.Names {
				out[id.Name] = true
			}
		}
		return true
	})
	return out
}

// goBuiltins are the predeclared functions and the conversions the scanned
// code uses.
var goBuiltins = map[string]bool{
	"append": true, "cap": true, "clear": true, "close": true, "complex": true, "copy": true, "delete": true,
	"imag": true, "len": true, "make": true, "max": true, "min": true, "new": true, "panic": true, "print": true,
	"println": true, "real": true, "recover": true,
	"bool": true, "byte": true, "error": true, "float64": true, "int": true, "int64": true, "int32": true,
	"rune": true, "string": true, "uint32": true, "uint64": true, "uintptr": true,
}

// fileWriteCalls are the os, syscall and unix calls that create, change or
// remove a file or directory, or read through links.
var (
	osWriters = map[string]bool{
		"Create": true, "CreateTemp": true, "Mkdir": true, "MkdirAll": true, "MkdirTemp": true, "Remove": true,
		"RemoveAll": true, "Rename": true, "Chmod": true, "Chown": true, "Lchown": true, "Chtimes": true,
		"WriteFile": true, "Link": true, "Symlink": true, "Truncate": true, "NewFile": true,
	}
	osFollowers  = map[string]bool{"Open": true, "ReadFile": true, "ReadDir": true, "DirFS": true, "Stat": true}
	sysWriters   = map[string]bool{"Open": true, "Openat": true, "Creat": true, "Unlink": true, "Unlinkat": true, "Rmdir": true, "Rename": true, "Renameat": true, "Renameat2": true, "Mkdir": true, "Mkdirat": true, "Mknod": true, "Mknodat": true, "Chmod": true, "Fchmod": true, "Fchmodat": true, "Chown": true, "Fchown": true, "Lchown": true, "Truncate": true, "Ftruncate": true, "Link": true, "Linkat": true, "Symlink": true, "Symlinkat": true, "Utimes": true, "UtimesNano": true, "Futimes": true, "Write": true, "Pwrite": true}
	walkFollower = map[string]bool{"Walk": true, "WalkDir": true}
)

type scanResult struct {
	violations []string
	seen       map[string]int
}

func (r *scanResult) has(fn string) bool {
	return slices.ContainsFunc(r.violations, func(v string) bool { return strings.Contains(v, " in "+fn+";") })
}

// --- TestHistoryExportReadsOnly ---

// exportCallsOutside are the functions of other files the export may call:
// each only stats.
var exportCallsOutside = map[string]bool{"sharedIsMounted": true}

// exprName is an identifier's name, or pkg.Name for a selector, or "".
func exprName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok {
			return x.Name + "." + e.Sel.Name
		}
	}
	return ""
}

// scanHistoryExport checks the export's files: every open goes through
// openBeneath, which alone opens a base directory (os.OpenFile with
// historyBaseFlags), walks below it with syscall.Openat (historyDirFlags, or
// its flags parameter for the last component) and wraps the descriptors with
// os.NewFile; every call of openBeneath passes historyOpenFlags or
// historyDirFlags (readCredentialReference alone opens outside HOME with
// os.O_RDONLY); no call creates, changes or removes a file, reads through a
// link, or walks with the standard library; and no call leaves the export's
// files except to exportCallsOutside.
func scanHistoryExport(t *testing.T, paths []string) scanResult {
	fset := token.NewFileSet()
	files := parseGoFiles(t, fset, paths)
	defined := definedFuncs(files)
	res := scanResult{seen: map[string]int{}}
	eachFunc(files, func(pf parsedFile, fn string, _ *ast.FuncDecl, body *ast.BlockStmt) {
		report := func(pos token.Pos, format string, args ...any) {
			res.violations = append(res.violations, fmt.Sprintf("%s: in %s; ", fset.Position(pos), fn)+fmt.Sprintf(format, args...))
		}
		local := localNames(body)
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok {
				if !defined[id.Name] && !local[id.Name] && !goBuiltins[id.Name] && !exportCallsOutside[id.Name] {
					report(call.Pos(), "calls %s, outside the export's files", id.Name)
				}
				if id.Name == "openBeneath" && fn != "openBeneath" {
					flags := ""
					if len(call.Args) > 2 {
						flags = exprName(call.Args[2])
					}
					if flags == "historyOpenFlags" || flags == "historyDirFlags" {
						res.seen["openBeneath "+flags+" in "+fn]++
					} else {
						report(call.Pos(), "calls openBeneath with flags %q, not historyOpenFlags or historyDirFlags", flags)
					}
				}
				return true
			}
			pkg, name, ok := pkgCall(pf, call)
			if !ok {
				return true
			}
			switch {
			case pkg == "os" && name == "OpenFile":
				flags := ""
				if len(call.Args) > 1 {
					flags = exprName(call.Args[1])
				}
				switch {
				case fn == "readCredentialReference" && flags == "os.O_RDONLY":
					res.seen["os.OpenFile os.O_RDONLY in "+fn]++
				case fn == "openBeneath" && flags == "historyBaseFlags":
					res.seen["os.OpenFile historyBaseFlags in "+fn]++
				default:
					report(call.Pos(), "opens with flags %q outside openBeneath", flags)
				}
			case (pkg == "syscall" || pkg == "unix") && name == "Openat" && fn == "openBeneath":
				flags := ""
				if len(call.Args) > 2 {
					flags = exprName(call.Args[2])
				}
				if flags == "historyDirFlags" || flags == "flags" {
					res.seen[pkg+".Openat "+flags+" in "+fn]++
				} else {
					report(call.Pos(), "opens below the base with flags %q, not historyDirFlags or its flags parameter", flags)
				}
			case pkg == "os" && name == "NewFile" && fn == "openBeneath":
				res.seen["os.NewFile in "+fn]++
			case pkg == "os" && osWriters[name]:
				report(call.Pos(), "calls os.%s, which writes", name)
			case pkg == "os" && osFollowers[name]:
				report(call.Pos(), "calls os.%s, which follows links", name)
			case (pkg == "syscall" || pkg == "unix") && sysWriters[name]:
				report(call.Pos(), "calls %s.%s", pkg, name)
			case pkg == "filepath" && walkFollower[name]:
				report(call.Pos(), "walks with filepath.%s, not readHistoryDir", name)
			}
			return true
		})
	})
	return res
}

func TestHistoryExportReadsOnly(t *testing.T) {
	res := scanHistoryExport(t, []string{"historyexport.go", "historyexport_layout.go"})
	for _, v := range res.violations {
		t.Error(v)
	}
	for want, n := range map[string]int{
		"os.OpenFile historyBaseFlags in openBeneath":          1,
		"syscall.Openat historyDirFlags in openBeneath":        1,
		"syscall.Openat flags in openBeneath":                  1,
		"os.NewFile in openBeneath":                            2,
		"openBeneath historyOpenFlags in openHistoryFileLinks": 1,
		"openBeneath historyDirFlags in readHistoryDir":        1,
		"os.OpenFile os.O_RDONLY in readCredentialReference":   1,
	} {
		if res.seen[want] != n {
			t.Errorf("%s: seen %d times, want %d", want, res.seen[want], n)
		}
	}
	if len(res.seen) != 7 {
		t.Errorf("the export opens files in more places than the scan expects: %v", res.seen)
	}
}

func TestHistoryExportScanCatchesWrites(t *testing.T) {
	res := scanHistoryExport(t, globGo(t, filepath.Join("testdata", "historyscan", "export")))
	for _, fn := range []string{
		"openWithoutNoFollow", "openWithLiteralFlags", "openPlain", "readFileFollows", "statFollows", "createFile",
		"removeFile", "renameFile", "chmodFile", "chtimesFile", "mkdirAll", "writeFile", "linkFile", "syscallUnlink",
		"walkFollows", "callsAWriterElsewhere", "readCredentialReference", "literalRemoves",
		"openOutsideOpenBeneath", "openBeneathWritable", "openatOutsideOpenBeneath", "newFileOutsideOpenBeneath",
		"openBeneath",
	} {
		if !res.has(fn) {
			t.Errorf("the export scan does not catch %s", fn)
		}
	}
}

// --- TestHistoryArchiveNeverRemoves ---

// archiveCallsOutside are the functions of other files the archive may call:
// they read, hash or stat.
var archiveCallsOutside = map[string]bool{
	"sharedIsMounted": true, "openHistoryFile": true, "openHistoryFileLinks": true, "readHistoryDir": true,
	"newBlockHasher": true, "hashHistoryFile": true,
}

// openWriteFlags are the open flags that let a call write.
var openWriteFlags = map[string]bool{"O_CREATE": true, "O_WRONLY": true, "O_RDWR": true, "O_TRUNC": true, "O_APPEND": true}

// incomingWriter reports whether fn may create files and directories: the
// incoming* functions, which write only under .incoming, and the two that
// publish from there.
func incomingWriter(fn string) bool {
	return strings.HasPrefix(fn, "incoming") || fn == "publishIncoming" || fn == "linkIndexEntry"
}

// startsWithCheckIncoming reports whether a body's first statement is
// `if err := a.checkIncoming(...); err != nil`.
func startsWithCheckIncoming(body *ast.BlockStmt) bool {
	if len(body.List) == 0 {
		return false
	}
	is, ok := body.List[0].(*ast.IfStmt)
	if !ok || is.Init == nil {
		return false
	}
	as, ok := is.Init.(*ast.AssignStmt)
	if !ok || len(as.Rhs) != 1 {
		return false
	}
	call, ok := as.Rhs[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "checkIncoming"
}

// scanHistoryArchive checks the archive's file: a removal only in
// removeIncoming, a rename only in publishIncoming, a hard link only in
// linkIndexEntry, a create or a write-mode open only in an incoming writer or
// lockHistoryArchive; each incoming writer, removeIncoming and
// publishIncoming checks its path is under .incoming first; nothing chmods,
// truncates or links symbolically; and no call leaves the file except to
// archiveCallsOutside.
func scanHistoryArchive(t *testing.T, paths []string) scanResult {
	fset := token.NewFileSet()
	files := parseGoFiles(t, fset, paths)
	defined := definedFuncs(files)
	res := scanResult{seen: map[string]int{}}
	eachFunc(files, func(pf parsedFile, fn string, decl *ast.FuncDecl, body *ast.BlockStmt) {
		report := func(pos token.Pos, format string, args ...any) {
			res.violations = append(res.violations, fmt.Sprintf("%s: in %s; ", fset.Position(pos), fn)+fmt.Sprintf(format, args...))
		}
		if decl != nil && fn != "checkIncoming" && (strings.HasPrefix(fn, "incoming") || fn == "removeIncoming" || fn == "publishIncoming") && !startsWithCheckIncoming(body) {
			report(body.Pos(), "does not check its path is under .incoming first")
		}
		local := localNames(body)
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok {
				if !defined[id.Name] && !local[id.Name] && !goBuiltins[id.Name] && !archiveCallsOutside[id.Name] {
					report(call.Pos(), "calls %s, outside the archive's file", id.Name)
				}
				return true
			}
			pkg, name, ok := pkgCall(pf, call)
			if !ok {
				return true
			}
			isOS := pkg == "os"
			isSys := pkg == "syscall" || pkg == "unix"
			switch {
			case isOS && (name == "Remove" || name == "RemoveAll") || isSys && (name == "Unlink" || name == "Unlinkat" || name == "Rmdir"):
				if fn != "removeIncoming" {
					report(call.Pos(), "removes outside removeIncoming")
				} else {
					res.seen["remove in "+fn]++
				}
			case isOS && name == "Rename" || isSys && strings.HasPrefix(name, "Rename"):
				if fn != "publishIncoming" {
					report(call.Pos(), "renames outside publishIncoming")
				} else {
					res.seen["rename in "+fn]++
				}
			case isOS && name == "Link" || isSys && (name == "Link" || name == "Linkat"):
				if fn != "linkIndexEntry" {
					report(call.Pos(), "links outside linkIndexEntry")
				} else {
					res.seen["link in "+fn]++
				}
			case isOS && (name == "Mkdir" || name == "MkdirAll"):
				if !incomingWriter(fn) {
					report(call.Pos(), "makes a directory outside the incoming writers")
				}
			case isOS && name == "OpenFile":
				writes := false
				if len(call.Args) > 1 {
					ast.Inspect(call.Args[1], func(n ast.Node) bool {
						if id, ok := n.(*ast.Ident); ok && openWriteFlags[id.Name] {
							writes = true
						}
						return true
					})
				}
				if writes && !strings.HasPrefix(fn, "incoming") && fn != "lockHistoryArchive" {
					report(call.Pos(), "opens for writing outside the incoming writers")
				}
			case isOS && osWriters[name], isSys && sysWriters[name]:
				report(call.Pos(), "calls %s.%s", pkg, name)
			case pkg == "filepath" && walkFollower[name]:
				report(call.Pos(), "walks with filepath.%s", name)
			}
			return true
		})
	})
	return res
}

func TestHistoryArchiveNeverRemoves(t *testing.T) {
	res := scanHistoryArchive(t, []string{"historyarchive.go"})
	for _, v := range res.violations {
		t.Error(v)
	}
	for _, want := range []string{"remove in removeIncoming", "rename in publishIncoming", "link in linkIndexEntry"} {
		if res.seen[want] != 1 {
			t.Errorf("%s: seen %d times, want once", want, res.seen[want])
		}
	}
}

func TestHistoryArchiveScanCatchesRemovals(t *testing.T) {
	res := scanHistoryArchive(t, globGo(t, filepath.Join("testdata", "historyscan", "archive")))
	for _, fn := range []string{
		"removeOutsideIncoming", "removeViaSyscall", "renameElsewhere", "linkElsewhere", "writeOutsideIncoming",
		"openForWriteOutsideIncoming", "mkdirOutsideIncoming", "chmodEntry", "truncateEntry", "incomingSneaky",
		"removeIncoming", "callsAWriterElsewhere", "symlinkEntry",
	} {
		if !res.has(fn) {
			t.Errorf("the archive scan does not catch %s", fn)
		}
	}
}

// --- TestHistoryLayoutCoversAgentdState ---

// knownNameFuncs are calls whose result is a state file's name, by a sample
// the classification must cover.
var knownNameFuncs = map[string]string{"apiv1.ContextName": "published-context.0123.4567"}

// stateScan finds every name agentd writes at the top of its state directory.
type stateScan struct {
	fset    *token.FileSet
	files   []parsedFile
	consts  map[string][]string
	funcs   map[string][]*ast.FuncDecl
	names   map[string][]string // top-level name -> sites
	report  func(pos token.Pos, fn, format string, args ...any)
	scanErr []string
}

func newStateScan(t *testing.T, paths []string) *stateScan {
	sc := &stateScan{fset: token.NewFileSet(), consts: map[string][]string{}, funcs: map[string][]*ast.FuncDecl{}, names: map[string][]string{}}
	sc.files = parseGoFiles(t, sc.fset, paths)
	sc.report = func(pos token.Pos, fn, format string, args ...any) {
		sc.scanErr = append(sc.scanErr, fmt.Sprintf("%s: in %s; ", sc.fset.Position(pos), fn)+fmt.Sprintf(format, args...))
	}
	for _, pf := range sc.files {
		for _, d := range pf.file.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				sc.funcs[d.Name.Name] = append(sc.funcs[d.Name.Name], d)
			case *ast.GenDecl:
				if d.Tok != token.CONST {
					continue
				}
				for _, spec := range d.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, n := range vs.Names {
						if i < len(vs.Values) {
							if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
								v, _ := strconv.Unquote(lit.Value)
								sc.consts[n.Name] = append(sc.consts[n.Name], v)
							}
						}
					}
				}
			}
		}
	}
	return sc
}

// resolve finds the string values an expression can have in fn: literals,
// constants, local assignments, range lists, and a parameter's arguments at
// every call of fn.
func (sc *stateScan) resolve(e ast.Expr, fn *ast.FuncDecl, depth int) ([]string, bool) {
	if depth > 4 {
		return nil, false
	}
	switch e := e.(type) {
	case *ast.ParenExpr:
		return sc.resolve(e.X, fn, depth)
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return nil, false
		}
		v, err := strconv.Unquote(e.Value)
		return []string{v}, err == nil
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return nil, false
		}
		l, ok1 := sc.resolve(e.X, fn, depth)
		r, ok2 := sc.resolve(e.Y, fn, depth)
		if !ok1 || !ok2 {
			return nil, false
		}
		var out []string
		for _, a := range l {
			for _, b := range r {
				out = append(out, a+b)
			}
		}
		return out, true
	case *ast.CallExpr:
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.Ident); ok {
				if v, ok := knownNameFuncs[x.Name+"."+sel.Sel.Name]; ok {
					return []string{v}, true
				}
			}
		}
		return nil, false
	case *ast.Ident:
		if v, ok := sc.consts[e.Name]; ok {
			return v, true
		}
		if fn == nil {
			return nil, false
		}
		var out []string
		found := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				for i, l := range n.Lhs {
					if id, ok := l.(*ast.Ident); ok && id.Name == e.Name && i < len(n.Rhs) && len(n.Lhs) == len(n.Rhs) {
						found = true
						v, ok := sc.resolve(n.Rhs[i], fn, depth+1)
						if !ok {
							out = nil
							found = false
							return false
						}
						out = append(out, v...)
					}
				}
			case *ast.RangeStmt:
				if id, ok := n.Value.(*ast.Ident); ok && id.Name == e.Name {
					if cl, ok := n.X.(*ast.CompositeLit); ok {
						found = true
						for _, elt := range cl.Elts {
							v, ok := sc.resolve(elt, fn, depth+1)
							if !ok {
								out = nil
								found = false
								return false
							}
							out = append(out, v...)
						}
					}
				}
			case *ast.ValueSpec:
				for i, id := range n.Names {
					if id.Name == e.Name && i < len(n.Values) {
						found = true
						v, ok := sc.resolve(n.Values[i], fn, depth+1)
						if !ok {
							out = nil
							found = false
							return false
						}
						out = append(out, v...)
					}
				}
			}
			return true
		})
		if found {
			return out, len(out) > 0
		}
		// A parameter: every caller's argument.
		idx := -1
		i := 0
		for _, field := range fn.Type.Params.List {
			for _, n := range field.Names {
				if n.Name == e.Name {
					idx = i
				}
				i++
			}
		}
		if idx < 0 {
			return nil, false
		}
		var vals []string
		calls := 0
		ok := true
		eachFunc(sc.files, func(_ parsedFile, _ string, caller *ast.FuncDecl, body *ast.BlockStmt) {
			ast.Inspect(body, func(n ast.Node) bool {
				call, isCall := n.(*ast.CallExpr)
				if !isCall || len(call.Args) <= idx {
					return true
				}
				name := ""
				switch f := call.Fun.(type) {
				case *ast.Ident:
					name = f.Name
				case *ast.SelectorExpr:
					name = f.Sel.Name
				}
				if name != fn.Name.Name {
					return true
				}
				calls++
				// A Settings whose StateDir the caller rebased under a
				// subdirectory (decisions.go's archive): the name is that
				// subdirectory's.
				if caller != nil {
					for _, a := range call.Args {
						if id, isID := a.(*ast.Ident); isID {
							if sub, rebased := sc.rebasedStateDir(id.Name, caller); rebased {
								v, good := sc.resolve(sub, caller, depth+1)
								if !good {
									ok = false
								}
								vals = append(vals, v...)
								return true
							}
						}
					}
				}
				v, good := sc.resolve(call.Args[idx], caller, depth+1)
				if !good {
					ok = false
				}
				vals = append(vals, v...)
				return true
			})
		})
		return vals, ok && calls > 0
	}
	return nil, false
}

// stateDirExpr reports whether e is agentd's state directory: a .StateDir
// field, a variable named stateDir, or a local assigned from a .StateDir.
func stateDirExpr(e ast.Expr, fn *ast.FuncDecl) bool {
	switch e := e.(type) {
	case *ast.SelectorExpr:
		return e.Sel.Name == "StateDir"
	case *ast.Ident:
		if e.Name == "stateDir" {
			return true
		}
		if fn == nil {
			return false
		}
		alias := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == len(as.Rhs) {
				for i, l := range as.Lhs {
					if id, ok := l.(*ast.Ident); ok && id.Name == e.Name {
						if sel, ok := as.Rhs[i].(*ast.SelectorExpr); ok && sel.Sel.Name == "StateDir" {
							alias = true
						}
					}
				}
			}
			return true
		})
		return alias
	}
	return false
}

// rebasedStateDir finds `recv.StateDir = filepath.Join(<state dir>, sub)` in
// fn: statePath on recv then writes under sub.
func (sc *stateScan) rebasedStateDir(recv string, fn *ast.FuncDecl) (ast.Expr, bool) {
	var sub ast.Expr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		sel, ok := as.Lhs[0].(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "StateDir" {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); !ok || x.Name != recv {
			return true
		}
		if call, ok := as.Rhs[0].(*ast.CallExpr); ok && len(call.Args) >= 2 && stateDirExpr(call.Args[0], fn) {
			sub = call.Args[1]
		}
		return true
	})
	return sub, sub != nil
}

func (sc *stateScan) add(pos token.Pos, fn string, decl *ast.FuncDecl, e ast.Expr, suffix string) {
	vals, ok := sc.resolve(e, decl, 0)
	if !ok {
		sc.report(pos, fn, "cannot resolve the name it writes under the state directory")
		return
	}
	for _, v := range vals {
		top := strings.SplitN(v+suffix, "/", 2)[0]
		sc.names[top] = append(sc.names[top], fmt.Sprintf("%s (%s)", sc.fset.Position(pos), fn))
	}
}

// tempHelper is a function that writes a temporary file beside the path it is
// given: os.CreateTemp(filepath.Dir(p), pattern) for its parameter p, or a
// call that passes p on to another tempHelper. writeFileAtomic, writeFileOnce,
// writeCodexPrivateAtomic and writeJSONFile are four.
type tempHelper struct {
	param    int
	patterns []tempPattern
}

// tempPattern is a CreateTemp pattern and the path parameter filepath.Base
// reads in it.
type tempPattern struct {
	expr  ast.Expr
	param string
}

func paramIndex(decl *ast.FuncDecl, name string) int {
	i := 0
	for _, field := range decl.Type.Params.List {
		for _, n := range field.Names {
			if n.Name == name {
				return i
			}
			i++
		}
	}
	return -1
}

// isPkgCall reports whether e is pkg.name(...) and returns the call.
func isPkgCall(e ast.Expr, pkg, name string) (*ast.CallExpr, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return nil, false
	}
	x, ok := sel.X.(*ast.Ident)
	return call, ok && x.Name == pkg
}

// calleeName is the name a call calls: f for f(...) and x.f(...).
func calleeName(call *ast.CallExpr) string {
	switch f := call.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// dirOfParam reports which parameter of decl e is the directory of:
// filepath.Dir(p), or a local assigned from it.
func dirOfParam(decl *ast.FuncDecl, e ast.Expr) (string, bool) {
	ofParam := func(e ast.Expr) (string, bool) {
		call, ok := isPkgCall(e, "filepath", "Dir")
		if !ok || len(call.Args) != 1 {
			return "", false
		}
		id, ok := call.Args[0].(*ast.Ident)
		return id.Name, ok && paramIndex(decl, id.Name) >= 0
	}
	if p, ok := ofParam(e); ok {
		return p, true
	}
	id, ok := e.(*ast.Ident)
	if !ok {
		return "", false
	}
	found := ""
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == len(as.Rhs) {
			for i, l := range as.Lhs {
				if lid, ok := l.(*ast.Ident); ok && lid.Name == id.Name {
					if p, ok := ofParam(as.Rhs[i]); ok {
						found = p
					}
				}
			}
		}
		return true
	})
	return found, found != ""
}

// findTempHelpers finds every tempHelper among the scanned functions.
func (sc *stateScan) findTempHelpers() map[string]*tempHelper {
	out := map[string]*tempHelper{}
	for name, decls := range sc.funcs {
		for _, decl := range decls {
			if decl.Body == nil {
				continue
			}
			ast.Inspect(decl.Body, func(n ast.Node) bool {
				call, ok := isPkgCall(asExpr(n), "os", "CreateTemp")
				if !ok || len(call.Args) != 2 {
					return true
				}
				if p, ok := dirOfParam(decl, call.Args[0]); ok {
					h := out[name]
					if h == nil {
						h = &tempHelper{param: paramIndex(decl, p)}
						out[name] = h
					}
					h.patterns = append(h.patterns, tempPattern{call.Args[1], p})
				}
				return true
			})
		}
	}
	for changed := true; changed; {
		changed = false
		for name, decls := range sc.funcs {
			if out[name] != nil {
				continue
			}
			for _, decl := range decls {
				if decl.Body == nil {
					continue
				}
				ast.Inspect(decl.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok || out[name] != nil {
						return out[name] == nil
					}
					h := out[calleeName(call)]
					if h == nil || len(call.Args) <= h.param {
						return true
					}
					if id, ok := call.Args[h.param].(*ast.Ident); ok && paramIndex(decl, id.Name) >= 0 {
						out[name] = &tempHelper{param: paramIndex(decl, id.Name), patterns: h.patterns}
						changed = true
						return false
					}
					return true
				})
			}
		}
	}
	return out
}

func asExpr(n ast.Node) ast.Expr {
	e, _ := n.(ast.Expr)
	return e
}

// statePathOf finds the names e can have at the top of the state directory:
// statePath(name), filepath.Join(<state dir>, name), or a local assigned from
// either. isState is false for anything else, and for a path in a
// subdirectory, where a temporary copy beside it is not at the top.
func (sc *stateScan) statePathOf(e ast.Expr, decl *ast.FuncDecl, depth int) (names []string, isState, ok bool) {
	if depth > 4 || decl == nil {
		return nil, false, true
	}
	switch e := e.(type) {
	case *ast.ParenExpr:
		return sc.statePathOf(e.X, decl, depth)
	case *ast.CallExpr:
		if sel, isSel := e.Fun.(*ast.SelectorExpr); isSel && sel.Sel.Name == "statePath" && len(e.Args) == 1 {
			if x, isID := sel.X.(*ast.Ident); isID {
				if _, rebased := sc.rebasedStateDir(x.Name, decl); rebased {
					return nil, false, true
				}
			}
			v, good := sc.resolve(e.Args[0], decl, 0)
			return v, true, good
		}
		if call, isJoin := isPkgCall(e, "filepath", "Join"); isJoin && len(call.Args) >= 2 && stateDirExpr(call.Args[0], decl) {
			if len(call.Args) > 2 {
				return nil, false, true
			}
			v, good := sc.resolve(call.Args[1], decl, 0)
			return v, true, good
		}
	case *ast.Ident:
		var out []string
		state, good := false, true
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			as, isAssign := n.(*ast.AssignStmt)
			if !isAssign || len(as.Lhs) != len(as.Rhs) {
				return true
			}
			for i, l := range as.Lhs {
				if id, isID := l.(*ast.Ident); isID && id.Name == e.Name {
					v, s, g := sc.statePathOf(as.Rhs[i], decl, depth+1)
					if s {
						state = true
						good = good && g
						out = append(out, v...)
					}
				}
			}
			return true
		})
		return out, state, good
	}
	return nil, false, true
}

// tempNames are the names a tempPattern gives beside the state file name.
func (sc *stateScan) tempNames(p tempPattern, name string) ([]string, bool) {
	var r func(e ast.Expr) ([]string, bool)
	r = func(e ast.Expr) ([]string, bool) {
		switch e := e.(type) {
		case *ast.ParenExpr:
			return r(e.X)
		case *ast.BasicLit:
			if e.Kind != token.STRING {
				return nil, false
			}
			v, err := strconv.Unquote(e.Value)
			return []string{v}, err == nil
		case *ast.Ident:
			v, ok := sc.consts[e.Name]
			return v, ok
		case *ast.BinaryExpr:
			if e.Op != token.ADD {
				return nil, false
			}
			l, ok1 := r(e.X)
			rr, ok2 := r(e.Y)
			if !ok1 || !ok2 {
				return nil, false
			}
			var out []string
			for _, a := range l {
				for _, b := range rr {
					out = append(out, a+b)
				}
			}
			return out, true
		case *ast.CallExpr:
			if call, ok := isPkgCall(e, "filepath", "Base"); ok && len(call.Args) == 1 {
				if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == p.param {
					return []string{path.Base(name)}, true
				}
			}
		}
		return nil, false
	}
	return r(p.expr)
}

// scanAgentdState collects the top-level names of every statePath call,
// filepath.Join(<state dir>, name, ...), os.CreateTemp(<state dir>, pattern)
// (each * as x), workspaceFileLock(<state dir>, name) (name.lock), and the
// temporary copy a tempHelper writes beside a state file.
func scanAgentdState(t *testing.T, paths []string) *stateScan {
	sc := newStateScan(t, paths)
	eachFunc(sc.files, func(pf parsedFile, fn string, decl *ast.FuncDecl, body *ast.BlockStmt) {
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "statePath" && len(call.Args) == 1 {
				if x, ok := sel.X.(*ast.Ident); ok && decl != nil {
					if sub, ok := sc.rebasedStateDir(x.Name, decl); ok {
						sc.add(call.Pos(), fn, decl, sub, "")
						return true
					}
				}
				sc.add(call.Pos(), fn, decl, call.Args[0], "")
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "workspaceFileLock" && len(call.Args) == 2 && stateDirExpr(call.Args[0], decl) {
				sc.add(call.Pos(), fn, decl, call.Args[1], ".lock")
				return true
			}
			pkg, name, ok := pkgCall(pf, call)
			if !ok || len(call.Args) < 2 || !stateDirExpr(call.Args[0], decl) {
				return true
			}
			switch {
			case pkg == "filepath" && name == "Join" && fn != "statePath":
				sc.add(call.Pos(), fn, decl, call.Args[1], "")
			case pkg == "os" && name == "CreateTemp":
				vals, ok := sc.resolve(call.Args[1], decl, 0)
				if !ok {
					sc.report(call.Pos(), fn, "cannot resolve the temporary name it writes under the state directory")
					return true
				}
				for _, v := range vals {
					top := strings.ReplaceAll(v, "*", "x")
					sc.names[top] = append(sc.names[top], fmt.Sprintf("%s (%s)", sc.fset.Position(call.Pos()), fn))
				}
			}
			return true
		})
	})
	helpers := sc.findTempHelpers()
	eachFunc(sc.files, func(_ parsedFile, fn string, decl *ast.FuncDecl, body *ast.BlockStmt) {
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			h := helpers[calleeName(call)]
			if h == nil || len(call.Args) <= h.param {
				return true
			}
			names, isState, good := sc.statePathOf(call.Args[h.param], decl, 0)
			if !isState {
				return true
			}
			if !good {
				sc.report(call.Pos(), fn, "cannot resolve the state file a temporary copy is written beside")
				return true
			}
			for _, name := range names {
				if strings.Contains(name, "/") {
					continue
				}
				for _, p := range h.patterns {
					vals, ok := sc.tempNames(p, name)
					if !ok {
						sc.report(call.Pos(), fn, "cannot resolve the temporary name written beside %s", name)
						continue
					}
					for _, v := range vals {
						top := strings.ReplaceAll(v, "*", "x")
						sc.names[top] = append(sc.names[top], fmt.Sprintf("%s (%s)", sc.fset.Position(call.Pos()), fn))
					}
				}
			}
			return true
		})
	})
	return sc
}

// unclassified are the scanned names agentdLayout does not classify.
func (sc *stateScan) unclassified() []string {
	var out []string
	for name, sites := range sc.names {
		if _, ok := classifyHistoryName(agentdLayout, name); !ok {
			out = append(out, fmt.Sprintf("%s, written at %s", name, strings.Join(sites, ", ")))
		}
	}
	sort.Strings(out)
	return out
}

func TestHistoryLayoutCoversAgentdState(t *testing.T) {
	sc := scanAgentdState(t, globGo(t, "."))
	for _, e := range sc.scanErr {
		t.Error(e)
	}
	for _, u := range sc.unclassified() {
		t.Errorf("agentd writes %s; classify it in agentdLayout", u)
	}
	// The scan must see the names it exists for, or it proves nothing: the
	// temporary copies included, through writeFileOnce, writeJSONFile and
	// writeCodexPrivateAtomic.
	for _, want := range []string{"launch.json", "boot.json", "task-result.json", "decisions", "managed-output", "initial-usage",
		"workspace-supervisor.lock", "native-invocation.lock", "published-context.0123.4567.json", "rescue-x.bundle", "rescue-index-x",
		".task-source.json.agentd-x", ".launch.json.agentd-x", ".codex-access-x"} {
		if len(sc.names[want]) == 0 {
			t.Errorf("the scan did not find %s", want)
		}
	}
	// Every rule names something agentd writes, so the table does not rot.
	for _, r := range agentdLayout {
		used := false
		for name := range sc.names {
			if ok, _ := path.Match(r.pattern, name); ok {
				used = true
				break
			}
		}
		if !used {
			t.Errorf("agentdLayout classifies %s, which no agentd code writes", r.pattern)
		}
	}
}

func TestHistoryLayoutScanCatchesNewState(t *testing.T) {
	sc := scanAgentdState(t, globGo(t, filepath.Join("testdata", "historyscan", "agentdstate")))
	report := strings.Join(append(sc.unclassified(), sc.scanErr...), "\n")
	for _, want := range []string{
		"unclassified-a.json", "unclassified-b.json", "unclassified-c", "unclassified-d", "unclassified-e-x",
		"unclassified-f.lock", "unclassified-g.json", "unclassified-h.json", "in dynamicName;", "unclassified-i",
		"unclassified-j", ".launch.json.unclassified-k-x", ".unclassified-l-x",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the state scan does not catch %s:\n%s", want, report)
		}
	}
}
