//go:build !darwin

package launch

// Elsewhere our window still makes room, and the person moves theirs into it.

func guiProcesses() ([]process, error) { return nil, ErrUnsupported }
func windowCount(int) (int, error)     { return 0, ErrUnsupported }
func place(int, Rect, bool) error      { return ErrUnsupported }
