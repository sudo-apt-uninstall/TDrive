package main

// Portable mode has to be switched on before anything resolves a path,
// and nothing was switching it on.
//
// backend/datadir/portable.go shipped on this branch complete: marker
// file, environment override, a writability check that creates a real
// file because Windows ACLs make stat lie, and a documented refusal to
// fall back to the user profile. It had no callers. A binary built from
// this branch behaved exactly like upstream, and the branch name said
// otherwise.
//
// # Why init
//
// An explicit call at the top of main() would be better and should
// replace this. It puts the startup sequence where a reader looks for
// it, and the package's own documentation asks for exactly that.
//
// The reason it is here instead is prosaic: the environment this was
// written in has no Go toolchain, main.go is ~24 KB, and rewriting a
// working file to insert three lines is a worse risk than adding a small
// one. That tradeoff expires the moment someone can run go build.
//
// init is nonetheless correct for this job rather than merely
// convenient. Every init in a package runs before main, and datadir
// resolves its bases lazily inside functions rather than at package
// initialisation, so no path can be read before this has run. The
// ordering hazard for this pattern is a package-level variable that
// calls datadir during its own initialisation; package main has none.

import (
	"fmt"
	"os"

	"TDrive/backend/datadir"
)

func init() {
	if err := applyPortableMode(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// applyPortableMode turns on portable mode when this installation asks
// for it, and stops the program when it asks and cannot have it.
//
// Every error is fatal, including the one that arrives with
// requested == false. That case means EnablePortable could not locate
// the executable, so it could not read the marker beside it, so it does
// not know whether this is a portable installation. Carrying on would
// pick the default location — which, if the marker was in fact there,
// writes the user's data to a system drive they are about to unplug
// themselves from and walk away.
//
// datadir/portable.go states the principle plainly: a portable
// application that quietly writes to the system drive is worse than one
// that refuses to start, because the user believes their data travelled
// with them and it did not. They find out later, somewhere else, with
// an empty drive. An executable that cannot find its own path is rare;
// paying for that rarity with a clear failure is the cheaper side of
// the trade.
func applyPortableMode() error {
	dir, requested, err := datadir.EnablePortable()
	switch {
	case err != nil && requested:
		// Asked for and undeliverable. The error from EnablePortable
		// already names the directory, the cause, and how to opt out by
		// removing the marker, so it is passed through rather than
		// wrapped into something vaguer.
		return fmt.Errorf("tdrive: %w", err)
	case err != nil:
		return fmt.Errorf("tdrive: could not determine whether this is a "+
			"portable installation: %w\n\nTDrive stops rather than guessing: "+
			"if a %s marker is present, guessing wrong would write your data "+
			"to this machine instead of beside the application. Set %s to the "+
			"directory you want, or run from a location where the executable "+
			"path can be resolved.",
			err, datadir.MarkerName, datadir.EnvDir)
	case requested:
		// Deliberately quiet on success. Announcing the directory on
		// every command would be noise in a CLI whose output is piped;
		// `tdrive status` is where this belongs, and datadir.Portable()
		// exposes it for exactly that.
		_ = dir
	}
	return nil
}
