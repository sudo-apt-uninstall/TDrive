package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"TDrive/backend/core"
	"TDrive/backend/datadir"
)

const stateFileName = "cli.json"

type state struct {
	CurrentDriveID int64             `json:"current_drive_id,omitempty"`
	CWDByDrive     map[string]string `json:"cwd_by_drive,omitempty"`
}

func newState() *state {
	return &state{CWDByDrive: make(map[string]string)}
}

func loadState() (*state, error) {
	path, err := statePath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return newState(), nil
	}
	if err != nil {
		return nil, err
	}

	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("read cli state: %w", err)
	}
	if st.CWDByDrive == nil {
		st.CWDByDrive = make(map[string]string)
	}
	return &st, nil
}

func (s *state) cwd(channelID int64) string {
	if s == nil {
		return "/"
	}
	cwd := s.CWDByDrive[strconv.FormatInt(channelID, 10)]
	clean, err := core.NormalizeRemotePath("/", cwd)
	if err != nil || clean == "" {
		return "/"
	}
	return clean
}

func (s *state) setCWD(channelID int64, cwd string) {
	if s.CWDByDrive == nil {
		s.CWDByDrive = make(map[string]string)
	}
	clean, err := core.NormalizeRemotePath("/", cwd)
	if err != nil || clean == "" {
		clean = "/"
	}
	s.CWDByDrive[strconv.FormatInt(channelID, 10)] = clean
}

func (s *state) save() error {
	path, err := statePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// statePath resolves cli.json through datadir rather than calling
// os.UserConfigDir directly.
//
// The two agree on an ordinary desktop install, which is why the direct
// call survived: it was never wrong, only unprotected. It becomes wrong
// the moment anything overrides the base -- mobile already does, and
// portable mode now does too. Without this, cli.json would stay behind
// in the user profile while every other TDrive file moved, and the
// installation would look portable while silently not being so.
func statePath() (string, error) {
	dir, err := datadir.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, stateFileName), nil
}
