//go:build cgo && !linux && (windows || darwin)

package nativeaudio

func linuxAudioActivity(string) Activity { return Activity{} }
