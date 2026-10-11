package datadir_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Files permitted to call the OS directory helpers. This is the whole
// implementation of the chokepoint and nothing else.
var chokepointAllowed = map[string]bool{
	filepath.Join("backend", "datadir", "datadir.go"):  true,
	filepath.Join("backend", "datadir", "portable.go"): true,
}

// The calls that must not appear outside the files above: the two that
// resolve TDrive's *own* directories, and the two that mobile and
// portable mode override.
//
// os.UserHomeDir is deliberately NOT here. The first version of this
// test included it and would have failed on correct code --
// cmd/tdrive/install_cli.go installs the CLI into the user's home, and
// an Android build script does the same. Neither resolves a TDrive data
// path; the home directory is the user's location, and asking the OS
// for it is exactly right when the destination really is the user's
// home.
//
// That mistake is worth leaving documented. A guard that fails on
// correct code is worse than no guard: the first thing anyone does with
// a test that blocks legitimate work is delete it, and the real
// protection goes with it.
var chokepointForbidden = map[string]bool{
	"UserConfigDir": true,
	"UserCacheDir":  true,
}

// TestNoProductionCodeBypassesDatadir fails if any production file
// resolves one of TDrive's own directories for itself.
//
// This exists because two files already did. Neither was exotic: both
// were written by someone who needed a config directory and reached for
// the obvious call, in a repository where the correct answer lives one
// import away. Nothing stopped them, so nothing would have stopped the
// next one -- and in a portable installation the consequence is files
// silently left behind on the wrong machine.
//
// The check walks the AST rather than grepping, because
// internal/app/app.go names both calls *in a comment* explaining why
// mobile overrides them. A substring search would fail on correct code,
// and the natural fix -- adding an exception for that file -- would
// punch a hole in the guarantee at exactly the place the reasoning is
// documented.
func TestNoProductionCodeBypassesDatadir(t *testing.T) {
	root := chokepointRepoRoot(t)
	var offenders []string

	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		// Test files are out of scope on purpose: several legitimately
		// drive the defaults to verify them, which is the opposite of
		// bypassing the chokepoint.
		if strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if chokepointAllowed[rel] {
			return nil
		}

		fset := token.NewFileSet()
		// Mode 0: comments are not attached, so a file that merely
		// mentions these calls in prose cannot be flagged.
		parsed, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok || pkg.Name != "os" {
				return true
			}
			if chokepointForbidden[selector.Sel.Name] {
				position := fset.Position(call.Pos())
				offenders = append(offenders,
					rel+":"+itoa(position.Line)+": os."+selector.Sel.Name)
			}
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk repository: %v", walkErr)
	}

	if len(offenders) > 0 {
		t.Fatalf("production code resolved a TDrive directory without datadir:\n  %s\n\n"+
			"Use datadir.Dir() or datadir.CacheDir(). They honour the\n"+
			"overrides mobile and portable mode depend on; calling the OS\n"+
			"helper directly leaves that file behind when every other one\n"+
			"moves, which looks portable and silently is not.",
			strings.Join(offenders, "\n  "))
	}
}

// TestTheGuardStillAllowsLegitimateHomeDirectoryUse is the inverse.
//
// A guard is only meaningful if something it permits is actually
// reached. Without this, narrowing the forbidden set to nothing at all
// would still pass the test above.
func TestTheGuardStillAllowsLegitimateHomeDirectoryUse(t *testing.T) {
	if chokepointForbidden["UserHomeDir"] {
		t.Fatal("os.UserHomeDir must stay permitted: installing the CLI into " +
			"the user's home is not a TDrive data path, and banning it fails " +
			"on correct code")
	}
	if len(chokepointForbidden) == 0 {
		t.Fatal("the forbidden set is empty, so the guard above cannot fail")
	}
}

// chokepointRepoRoot walks up from the test's working directory to the
// module root, so the test does not care where `go test` was invoked.
func chokepointRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod above the test's working directory")
		}
		dir = parent
	}
}

// itoa avoids pulling strconv in for one call site.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}
