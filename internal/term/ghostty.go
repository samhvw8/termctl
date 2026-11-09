package term

// ghostty is a stub. It fails loudly so nobody mistakes it for a working backend.
type ghostty struct{}

var errGhostty = BackendErr("not supported: Ghostty has no control API yet")

func (ghostty) Name() string                                       { return "ghostty" }
func (ghostty) ListPanes() ([]*Pane, error)                        { return nil, errGhostty }
func (ghostty) Spawn(where, cmd, cwd, title string) (*Pane, error) { return nil, errGhostty }
func (ghostty) SendText(*Pane, string) error                       { return errGhostty }
func (ghostty) SendKey(*Pane, string) error                        { return errGhostty }
func (ghostty) Capture(*Pane, int) ([]string, error)               { return nil, errGhostty }
func (ghostty) Close(*Pane) error                                  { return errGhostty }
func (ghostty) SetLabel(*Pane, string) error                       { return errGhostty }
func (ghostty) SelfPane() (*Pane, error)                           { return nil, errGhostty }
func (ghostty) NativeStatus(*Pane) string                          { return "" }
func (ghostty) Available() bool                                    { return false }
func (ghostty) ServerRef() map[string]string                       { return nil }
