package server

import (
	"fmt"
	"strings"
)

// Mode selects which areas of EasyLab a process serves.
type Mode string

const (
	// ModeAll serves the admin UI and the student portal from one process. It is
	// the default, and what every deployment ran before the areas could be split.
	ModeAll Mode = "all"
	// ModeAdmin serves the admin UI only, plus the OAuth broker endpoints the
	// in-lab student portals sign students in through.
	ModeAdmin Mode = "admin"
	// ModeStudent serves the student portal of a single lab, from inside that
	// lab's own cluster (see portal_runtime.go).
	ModeStudent Mode = "student"
)

// EnvMode is the environment variable the run mode defaults from.
const EnvMode = "EASYLAB_MODE"

// ParseMode converts a flag/env value into a Mode. Empty means ModeAll.
func ParseMode(s string) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case "", ModeAll:
		return ModeAll, nil
	case ModeAdmin:
		return ModeAdmin, nil
	case ModeStudent:
		return ModeStudent, nil
	default:
		return "", fmt.Errorf("unknown mode %q (expected %s, %s or %s)", s, ModeAll, ModeAdmin, ModeStudent)
	}
}

// ServesAdmin reports whether the admin UI is part of this mode.
func (m Mode) ServesAdmin() bool { return m == ModeAll || m == ModeAdmin }

// ServesStudent reports whether the student portal is part of this mode.
func (m Mode) ServesStudent() bool { return m == ModeAll || m == ModeStudent }
