//go:build linux || openbsd

package desktop

import "golang.design/x/hotkey"

func platformModifier(value string) (hotkey.Modifier, bool) {
	switch value {
	case "ctrl", "control":
		return hotkey.ModCtrl, true
	case "shift":
		return hotkey.ModShift, true
	case "alt", "option":
		return hotkey.Mod1, true
	case "win", "cmd", "command", "meta", "super":
		return hotkey.Mod4, true
	default:
		return 0, false
	}
}
