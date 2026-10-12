package datadir_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Files permitted to resolve an OS-chosen location. This is the whole
// implementation of the chokepoint and nothing else.
var chokepointAllowed = map[string]bool{
	filepath.Join("backend", "datadir", "datadir.go"):  true,
	filepath.Join("backend", "datadir", "portable.go"): true,
}

// Calls that resolve an OS-chosen directory whatever their arguments are.
//
// os.UserHomeDir is deliberately absent. The first version of this test
// included it and would have failed on correct code --
// cmd/tdrive/install_cli.go installs the CLI into the user's home, and an
// Android build script does the same. Neither resolves a TDrive data
// path; the home directory is the user's location, and asking the OS for
// it is exactly right when the destination really is the user's home.
//
// That mistake is worth leaving documented, because the fix below walked
// straight back into it. A guard that fails on correct code is worse than
// no guard: the first thing anyone does with a test that blocks
// legitimate work is delete it, and the real protection goes with it.
var chokepointAlwaysForbidden = map[string]bool{
	"UserConfigDir": true,
	"UserCacheDir":  true,
	"TempDir":       true,
}

// Calls whose first argument is a parent directory, and which are only a
// bypass when that argument is the empty string.
//
// os.MkdirTemp(parent, pattern) with a real parent is not merely
// acceptable, it is the pattern this repository should prefer:
// backend/services/file/download_staging.go stages into a sibling of the
// destination, so publishing is an atomic rename on the same filesystem
// and the staging directory follows the destination wherever the user
// put it. That is portable without anyone having to think about it.
//
// os.MkdirTemp("", pattern) is the same call meaning something else
// entirely: put this in the system temp directory, wherever the OS says
// that is, ignoring every override portable mode and mobile depend on.
//
// One function, two meanings, told apart only by an argument. Checking
// the name alone would have banned the good one along with the bad.
var chokepointForbiddenWhenRootIsEmpty = map[string]bool{
	"MkdirTemp":  true,
	"CreateTemp": true,
}

// chokepointOffendersIn reports every forbidden call in one parsed file.
//
// Shared by the walk below and by the inverse tests, deliberately: an
// inverse test that exercises a different code path from the real check
// proves nothing about the real check.
func chokepointOffendersIn(fset *token.FileSet, file *ast.File, rel string) []string {
	var offenders []string
	ast.Inspect(file, func(node ast.Node) bool {
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

		name := selector.Sel.Name
		forbidden := false
		switch {
		case chokepointAlwaysForbidden[name]:
			forbidden = true
		case chokepointForbiddenWhenRootIsEmpty[name]:
			forbidden = len(call.Args) > 0 && chokepointIsEmptyStringLiteral(call.Args[0])
		}
		if !forbidden {
			return true
		}

		position := fset.Position(call.Pos())
		offenders = append(offenders,
			rel+":"+strconv.Itoa(position.Line)+": os."+name)
		return true
	})
	return offenders
}

// chokepointIsEmptyStringLiteral reports whether an expression is a
// string literal with no content, in either quoting style.
//
// A variable that happens to hold "" is not caught, and cannot be without
// type checking the whole program. The guard is a tripwire for the
// obvious mistake, not a proof; it is worth being honest that the
// obvious mistake is the one that shipped seven times.
func chokepointIsEmptyStringLiteral(expr ast.Expr) bool {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return false
	}
	unquoted, err := strconv.Unquote(literal.Value)
	if err != nil {
		return false
	}
	return unquoted == ""
}

// TestNoProductionCodeBypassesDatadir fails if any production file
// resolves one of TDrive's own directories for itself.
//
// This exists because files already did, and the previous version of this
// test did not notice. It checked os.UserConfigDir and os.UserCacheDir
// and nothing else, while seven production files called
// os.MkdirTemp("", ...) -- including three on thumbnail and media paths,
// which run every time a gallery is browsed. The test passed. A guard
// that passes on the defect it was written to catch does not merely fail
// to protect: it certifies.
//
// The check walks the AST rather than grepping, because
// internal/app/app.go names these calls *in a comment* explaining why
// mobile overrides them. A substring search would fail on correct code,
// and the natural fix -- an exception for that file -- would punch a hole
// in the guarantee at exactly the place the reasoning is documented.
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
		offenders = append(offenders, chokepointOffendersIn(fset, parsed, rel)...)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk repository: %v", walkErr)
	}

	if len(offenders) > 0 {
		t.Fatalf("production code resolved a directory without datadir:\n  %s\n\n"+
			"Use datadir.Dir(), datadir.CacheDir() or datadir.CreateCacheTemp().\n"+
			"They honour the overrides mobile and portable mode depend on.\n\n"+
			"If you need a temporary file, prefer a sibling of the file it will\n"+
			"become: os.MkdirTemp(parent, pattern) with a real parent keeps the\n"+
			"publish step an atomic rename on one filesystem, and follows the\n"+
			"destination wherever the user put it. Passing \"\" asks the OS for\n"+
			"its temp directory instead, which looks portable and silently is\n"+
			"not.",
			strings.Join(offenders, "\n  "))
	}
}

// TestTheGuardFiresOnAKnownBypass is the inverse, and it is the point.
//
// The previous inverse test asserted facts about the contents of a map.
// That would still have passed with the detector entirely broken, which
// is roughly what happened: the forbidden set was non-empty, the test was
// green, and seven bypasses were in the tree.
//
// This one runs the detector that the walk above runs, over source that
// is known to be a bypass, and requires it to fire.
func TestTheGuardFiresOnAKnownBypass(t *testing.T) {
	const bypass = `package probe

import "os"

func scratch() (string, error) {
	return os.MkdirTemp("", "tdrive-probe-*")
}

func cacheRoot() (string, error) {
	return os.UserConfigDir()
}

func systemTemp() string {
	return os.TempDir()
}
`

	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, "probe.go", bypass, 0)
	if err != nil {
		t.Fatalf("parse probe source: %v", err)
	}

	offenders := chokepointOffendersIn(fset, parsed, "probe.go")
	if len(offenders) != 3 {
		t.Fatalf("guard found %d offenders in source containing three, %v\n\n"+
			"The walk in this file shares this detector. If it is blind here it\n"+
			"is blind there, and a green run means nothing.",
			len(offenders), offenders)
	}
}

// TestTheGuardStaysSilentOnCorrectCode is the other half, and it is the
// half that is easy to forget.
//
// Every shape below is real, is in this repository, and is right. If the
// guard flags any of them it will be deleted by the first person it
// blocks, and the protection goes with it.
func TestTheGuardStaysSilentOnCorrectCode(t *testing.T) {
	const correct = `package probe

import (
	"os"
	"path/filepath"
)

// Staging a sibling of the destination: publishing is an atomic rename on
// the same filesystem, and it follows the destination wherever it is.
func folderStaging(parent string) (string, error) {
	return os.MkdirTemp(parent, ".tdrive-folder-download-*")
}

// A temporary file adjacent to the file it will become.
func downloadTemp(destination string) (*os.File, error) {
	return os.CreateTemp(filepath.Dir(destination), ".tdrive-download-*")
}

// The user's own home directory, for installing the CLI into it. Not a
// TDrive data path, and asking the OS is exactly right.
func cliInstallTarget() (string, error) {
	return os.UserHomeDir()
}

// Ordinary file access, named similarly enough to be worth including.
func readSource(path string) (*os.File, error) {
	return os.Open(path)
}
`

	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, "probe.go", correct, 0)
	if err != nil {
		t.Fatalf("parse probe source: %v", err)
	}

	if offenders := chokepointOffendersIn(fset, parsed, "probe.go"); len(offenders) != 0 {
		t.Fatalf("guard flagged correct code: %v\n\n"+
			"os.MkdirTemp(parent, ...) with a real parent is the pattern to\n"+
			"prefer, not the one to ban. os.UserHomeDir resolves the user's\n"+
			"home, which is not a TDrive directory.",
			offenders)
	}
}

// TestTheForbiddenSetsAreNotEmpty keeps the two tests above honest.
//
// Emptying both maps would make the walk pass and the correct-code test
// pass; only the bypass test would fail. Cheap insurance, and it states
// the dependency out loud.
func TestTheForbiddenSetsAreNotEmpty(t *testing.T) {
	if len(chokepointAlwaysForbidden) == 0 {
		t.Fatal("the always-forbidden set is empty, so the walk cannot fail")
	}
	if len(chokepointForbiddenWhenRootIsEmpty) == 0 {
		t.Fatal("the empty-root set is empty: the seven bypasses this guard " +
			"was rewritten to catch would pass again")
	}
	if chokepointAlwaysForbidden["UserHomeDir"] || chokepointForbiddenWhenRootIsEmpty["UserHomeDir"] {
		t.Fatal("os.UserHomeDir must stay permitted: installing the CLI into " +
			"the user's home is not a TDrive data path, and banning it fails " +
			"on correct code")
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
