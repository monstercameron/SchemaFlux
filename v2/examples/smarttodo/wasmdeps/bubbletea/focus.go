package tea

// FocusMsg reports that the terminal gained focus. Bubbles v1 uses this
// message even when the embedded Bubble Tea renderer does not emit it.
type FocusMsg struct{}

// BlurMsg reports that the terminal lost focus.
type BlurMsg struct{}
