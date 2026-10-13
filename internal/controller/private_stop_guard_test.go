package controller

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
)

// TestOnlyStopPrivateExecutorPatchesPods is DESIGN-025 section 11's guard for
// the stop request. It reads every non-test Go file of the module. Apart from
// creating a Pod and the guarded deletes (podDeleters), the only Pod write is
// the one Patch in stopPrivateExecutor. That patch is a JSON patch built by
// privateStopPatch, which holds exactly two tests and the one
// /spec/activeDeadlineSeconds operation. stopPrivateExecutor has one caller,
// and that caller asks stopIntentConfirmed first. stopPrivateExecutor takes a
// confirmedStopIntent and refuses its zero value; a non-zero one is built only
// in stopIntentConfirmed, after its confirmed status write, and no code
// outside it sets the intent's fields. So the gate is the type: no patch
// without a confirmed intent, whatever the caller does with errors.
//
// The scan reads syntax, not types. An object counts as a Pod when it is
// declared as a corev1 Pod (a parameter, a var, a composite literal or a
// DeepCopy of one) or when an identifier in it names a pod. A typed clientset's
// Pods(...) chain and a REST chain on Resource("pods") count too.
// TestPodWriteScanCatchesPodWrites runs the scan over testdata/podwritescan,
// where each function writes a Pod in a different way, and the scan must name
// every one. Known gaps: a Pod handed in from another function through a value
// that does not say "pod", and a write made through reflection or another
// module. Review covers those.
func TestOnlyStopPrivateExecutorPatchesPods(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found at %s: %v", root, err)
	}
	var files []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (name == "testdata" || name == "bin" || name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	scan := scanPodWrites(t, files)
	for _, v := range scan.violations {
		t.Error(v)
	}
	// The scan must see the one allowed patch, the one allowed delete and the
	// one guarded call, or it proves nothing.
	wants := []string{"pod Patch in stopPrivateExecutor", "stopPrivateExecutor in requestPrivateExecutorStop", "confirmedStopIntent in stopIntentConfirmed"}
	for fn := range podDeleters {
		wants = append(wants, "pod Delete in "+fn)
	}
	for _, want := range wants {
		if n := scan.seen[want]; n != 1 {
			t.Errorf("%s: seen %d times, want once", want, n)
		}
	}
	if len(files) < 50 {
		t.Fatalf("scanned only %d files; the module walk is broken", len(files))
	}

	// The patch itself: exactly two tests and one add, in this order.
	got := privateStopPatch("executor-uid", "42", 3630)
	want := []jsonPatchOp{
		{Op: "test", Path: "/metadata/uid", Value: "executor-uid"},
		{Op: "test", Path: "/metadata/resourceVersion", Value: "42"},
		{Op: "add", Path: "/spec/activeDeadlineSeconds", Value: int64(3630)},
	}
	if !apiequality.Semantic.DeepEqual(got, want) {
		t.Fatalf("the stop patch is %+v, want exactly %+v", got, want)
	}
}

// TestPodWriteScanCatchesPodWrites runs the scan over testdata/podwritescan, a
// file that is never compiled. Each function there breaks one rule, and the
// scan must name every one.
func TestPodWriteScanCatchesPodWrites(t *testing.T) {
	scan := scanPodWrites(t, []string{filepath.Join("testdata", "podwritescan", "podwrites.go")})
	for _, fn := range []string{
		"patchPodElsewhere", "updateDeclaredPod", "statusUpdatePodParam", "ephemeralContainersUpdate", "evictionCreate",
		"deepCopyThenPatch", "patchOwnedObj", "typedClientsetPatch", "typedClientsetEvict", "restClientPatch",
		"deletePodElsewhere", "applyPod", "stopWithoutIntent", "stopIntentAfterStop", "forgedIntent", "intentFieldWrite",
	} {
		if !slices.ContainsFunc(scan.violations, func(v string) bool { return strings.Contains(v, " in "+fn+";") }) {
			t.Errorf("the pod write scan does not catch %s", fn)
		}
	}
	// stopPrivateExecutor there sends a merge patch, and a second patch, and
	// builds no privateStopPatch: each is named.
	for _, want := range []string{"not a JSON patch", "more than one pod patch", "privateStopPatch", "before its confirmed status write"} {
		if !slices.ContainsFunc(scan.violations, func(v string) bool {
			return (strings.Contains(v, " in stopPrivateExecutor;") || strings.Contains(v, " in stopIntentConfirmed;")) && strings.Contains(v, want)
		}) {
			t.Errorf("the pod write scan does not catch stopPrivateExecutor or stopIntentConfirmed: %s", want)
		}
	}
}

// podDeleters are the guarded deletes of a Pod, each after its own guard
// (TestOnlyTheGuardDeletes): deletePod (D-45) and the HOME expiry's two
// exceptions, the ended executor and the expiry's hold pod (DESIGN-025 5.2,
// 6.1).
var podDeleters = map[string]bool{"deletePod": true, "deleteExpiryExecutor": true, "deleteExpiryHold": true}

type podWriteScan struct {
	violations []string
	seen       map[string]int
}

// podWriteVerbs are the controller-runtime client methods that write the object
// they are given. Create is allowed for a Pod (that is how a session gets one);
// on a subresource it is a write to an existing Pod (eviction, binding).
var podWriteVerbs = map[string]bool{"Patch": true, "Update": true, "Apply": true, "Delete": true, "DeleteAllOf": true}

// clientsetPodWrites are the typed clientset's Pod methods that change an
// existing Pod.
var clientsetPodWrites = map[string]bool{
	"Patch": true, "Update": true, "UpdateStatus": true, "UpdateEphemeralContainers": true, "UpdateResize": true,
	"Apply": true, "ApplyStatus": true, "Delete": true, "DeleteCollection": true, "Evict": true, "EvictV1": true,
	"EvictV1beta1": true, "Bind": true,
}

// restReadOnlySubresources are the Pod subresources a REST Post may reach
// without writing the Pod object: exec, attach and port forwarding.
var restReadOnlySubresources = map[string]bool{"exec": true, "attach": true, "portforward": true}

func scanPodWrites(t *testing.T, files []string) podWriteScan {
	t.Helper()
	fset := token.NewFileSet()
	scan := podWriteScan{seen: map[string]int{}}
	report := func(pos token.Pos, fn, format string, args ...any) {
		scan.violations = append(scan.violations, fmt.Sprintf("%s: in %s; ", fset.Position(pos), fn)+fmt.Sprintf(format, args...))
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		core := coreV1Names(file)
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Body != nil {
					scanPodWriteBody(&scan, report, core, d.Name.Name, d.Type, d.Body)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok && len(vs.Names) > 0 {
						for _, v := range vs.Values {
							ast.Inspect(v, func(n ast.Node) bool {
								if lit, ok := n.(*ast.FuncLit); ok {
									scanPodWriteBody(&scan, report, core, vs.Names[0].Name, lit.Type, lit.Body)
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
	return scan
}

// coreV1Names are the names a file imports k8s.io/api/core/v1 under.
func coreV1Names(file *ast.File) map[string]bool {
	names := map[string]bool{}
	for _, imp := range file.Imports {
		if path, _ := strconv.Unquote(imp.Path.Value); path == "k8s.io/api/core/v1" {
			if imp.Name != nil {
				names[imp.Name.Name] = true
			} else {
				names["v1"] = true
			}
		}
	}
	return names
}

// isPodType reports whether e is corev1.Pod, *corev1.Pod or a slice of them.
func isPodType(e ast.Expr, core map[string]bool) bool {
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.ArrayType:
			e = x.Elt
		case *ast.ParenExpr:
			e = x.X
		case *ast.SelectorExpr:
			id, ok := x.X.(*ast.Ident)
			return ok && core[id.Name] && (x.Sel.Name == "Pod" || x.Sel.Name == "PodList")
		default:
			return false
		}
	}
}

// mentionsPod reports whether an identifier in e names a pod, or e is an
// identifier declared as one.
func mentionsPod(e ast.Expr, pods map[string]bool) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && (pods[id.Name] || strings.Contains(strings.ToLower(id.Name), "pod")) {
			found = true
		}
		return !found
	})
	return found
}

// podValue reports whether e builds or copies a Pod: a corev1 Pod composite
// literal, or a DeepCopy of a Pod.
func podValue(e ast.Expr, core, pods map[string]bool) bool {
	for {
		switch x := e.(type) {
		case *ast.UnaryExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.CompositeLit:
			return isPodType(x.Type, core)
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "DeepCopy" {
				return mentionsPod(sel.X, pods)
			}
			return false
		default:
			return false
		}
	}
}

// chain returns the calls of a method chain from the outermost in: for
// a.B().C(), [C(...), B()].
func chain(call *ast.CallExpr) []*ast.CallExpr {
	var out []*ast.CallExpr
	for e := ast.Expr(call); ; {
		c, ok := e.(*ast.CallExpr)
		if !ok {
			return out
		}
		out = append(out, c)
		sel, ok := c.Fun.(*ast.SelectorExpr)
		if !ok {
			return out
		}
		e = sel.X
	}
}

func stringArg(c *ast.CallExpr) string {
	if len(c.Args) != 1 {
		return ""
	}
	lit, ok := c.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	s, _ := strconv.Unquote(lit.Value)
	return s
}

func scanPodWriteBody(scan *podWriteScan, report func(token.Pos, string, string, ...any), core map[string]bool, fn string, typ *ast.FuncType, body *ast.BlockStmt) {
	pods := map[string]bool{}
	if typ != nil && typ.Params != nil {
		for _, field := range typ.Params.List {
			if isPodType(field.Type, core) {
				for _, n := range field.Names {
					pods[n.Name] = true
				}
			}
		}
	}
	first := map[string]token.Pos{}
	podPatches := 0
	sawStopPatch := false
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.ValueSpec:
			for i, name := range x.Names {
				if x.Type != nil && isPodType(x.Type, core) || i < len(x.Values) && podValue(x.Values[i], core, pods) {
					pods[name.Name] = true
				}
			}
		case *ast.AssignStmt:
			if len(x.Lhs) == len(x.Rhs) {
				for i, lhs := range x.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && podValue(x.Rhs[i], core, pods) {
						pods[id.Name] = true
					}
				}
			}
			for _, lhs := range x.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok && (sel.Sel.Name == "confirmedSession" || sel.Sel.Name == "readExecutor") && fn != "stopIntentConfirmed" {
					report(sel.Pos(), fn, "a write to a stop intent's .%s outside stopIntentConfirmed", sel.Sel.Name)
				}
			}
		case *ast.CompositeLit:
			if id, ok := x.Type.(*ast.Ident); ok && id.Name == "confirmedStopIntent" && len(x.Elts) > 0 {
				if fn != "stopIntentConfirmed" {
					report(x.Pos(), fn, "a confirmedStopIntent built outside stopIntentConfirmed")
				} else {
					scan.seen["confirmedStopIntent in stopIntentConfirmed"]++
					if pos, ok := first["writePrivateHistoryAcknowledged"]; !ok || pos > x.Pos() {
						report(x.Pos(), fn, "a confirmedStopIntent built before its confirmed status write (writePrivateHistoryAcknowledged)")
					}
				}
			}
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calleeName(call)
		if _, ok := first[name]; !ok {
			first[name] = call.Pos()
		}
		calls := chain(call)
		// The stop: one caller, after its confirmed intent.
		if name == "stopPrivateExecutor" {
			if fn == "requestPrivateExecutorStop" {
				scan.seen["stopPrivateExecutor in requestPrivateExecutorStop"]++
			} else {
				report(call.Pos(), fn, "stopPrivateExecutor is called outside requestPrivateExecutorStop")
			}
			if pos, ok := first["stopIntentConfirmed"]; !ok || pos > call.Pos() {
				report(call.Pos(), fn, "stopPrivateExecutor is called before stopIntentConfirmed is asked")
			}
			return true
		}
		if name == "privateStopPatch" && fn == "stopPrivateExecutor" {
			sawStopPatch = true
		}
		// A typed clientset's Pods(...) chain.
		if clientsetPodWrites[name] {
			for _, c := range calls[1:] {
				if calleeName(c) == "Pods" {
					report(call.Pos(), fn, "a typed clientset Pod write (%s); the only Pod write is stopPrivateExecutor's patch", name)
					return true
				}
			}
		}
		// A REST chain on Resource("pods").
		restPods, verb, sub := false, "", ""
		for _, c := range calls {
			switch calleeName(c) {
			case "Resource":
				restPods = restPods || stringArg(c) == "pods"
			case "SubResource":
				sub = stringArg(c)
			case "Patch", "Put", "Delete", "Post", "Get":
				if len(c.Args) <= 1 {
					verb = calleeName(c)
				}
			}
		}
		allowedPost := verb == "Post" && (sub == "" || restReadOnlySubresources[sub])
		if restPods && verb != "" && verb != "Get" && !allowedPost {
			report(call.Pos(), fn, "a REST %s on pods (subresource %q); the only Pod write is stopPrivateExecutor's patch", verb, sub)
			return true
		}
		// controller-runtime: Patch, Update, Apply and Delete of a Pod, on
		// the object or a subresource; Create on a Pod's subresource.
		onSub := len(calls) > 1 && (calleeName(calls[1]) == "Status" || calleeName(calls[1]) == "SubResource")
		writes := podWriteVerbs[name] || name == "Create" && onSub
		if !writes || len(call.Args) < 2 {
			return true
		}
		if !mentionsPod(call.Args[1], pods) {
			return true
		}
		switch {
		case name == "Patch" && !onSub && fn == "stopPrivateExecutor":
			podPatches++
			scan.seen["pod Patch in stopPrivateExecutor"]++
			if podPatches > 1 {
				report(call.Pos(), fn, "more than one pod patch")
			}
			if len(call.Args) < 3 || !isJSONPatch(call.Args[2]) {
				report(call.Pos(), fn, "the stop patch is not a JSON patch (client.RawPatch(types.JSONPatchType, ...))")
			}
			if !sawStopPatch {
				report(call.Pos(), fn, "the stop patch is not built by privateStopPatch before it is sent")
			}
		case name == "Delete" && !onSub && podDeleters[fn]:
			scan.seen["pod Delete in "+fn]++
		default:
			report(call.Pos(), fn, "a Pod %s; apart from create and the guarded deletes, the only Pod write is stopPrivateExecutor's patch", name)
		}
		return true
	})
}

// isJSONPatch reports whether e is client.RawPatch(types.JSONPatchType, ...).
func isJSONPatch(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok || calleeName(call) != "RawPatch" || len(call.Args) != 2 {
		return false
	}
	sel, ok := call.Args[0].(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "JSONPatchType"
}
