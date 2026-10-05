//go:build windows

package desktop

import "golang.design/x/hotkey"

func platformModifier(value string) (hotkey.Modifier, bool) {
	switch value {
	case "ctrl", "control":
		return hotkey.ModCtrl, true
	case "shift":
		return hotkey.ModShift, true
	case "alt", "option":
		return hotkey.ModAlt, true
	case "win", "cmd", "command", "meta":
		return hotkey.ModWin, true
	default:
		return 0, false
	}
}
