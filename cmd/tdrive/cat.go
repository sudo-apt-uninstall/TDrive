package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"TDrive/backend/datadir"
)

// catTempPath picks the scratch location `cat` downloads into before
// streaming it to stdout.
//
// It used os.MkdirTemp(""), which asks the OS for its temp directory and
// therefore ignores both portable mode and the mobile overrides. On a
// portable installation that wrote the contents of a remote file to the
// host machine's system drive -- briefly, but on a machine the user is
// about to walk away from, which is the exact failure portable mode
// exists to prevent.
//
// The cache root is the right home. datadir resolves it through the one
// chokepoint so it follows the installation, and CleanupCacheTemps
// already sweeps that root at startup if a crash leaves something
// behind.
func catTempPath() (string, func() error, error) {
	dir, err := datadir.CreateCacheTempDir("tdrive-cat-*")
	if err != nil {
		return "", nil, err
	}
	return filepath.Join(dir, "content"), func() error { return os.RemoveAll(dir) }, nil
}

func runCat(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: tdrive cat <remote-file>")
	}
	tmpPath, cleanup, err := catTempPath()
	if err != nil {
		return err
	}
	defer func() {
		if err := cleanup(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove temporary download: %v\n", err)
		}
	}()

	c, err := newDaemonClient()
	if err != nil {
		return err
	}
	if _, err := c.DownloadInDrive(cliCurrentOptions().DriveID, args[0], tmpPath, printTransferEvent); err != nil {
		fmt.Fprintln(os.Stderr)
		return err
	}
	fmt.Fprintln(os.Stderr)

	f, err := os.Open(tmpPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	// Unix can unlink the verified file while stdout still reads its open handle.
	_ = os.Remove(tmpPath)
	_, err = io.Copy(os.Stdout, f)
	return err
}
