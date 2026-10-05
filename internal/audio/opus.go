package audio

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pion/opus"
)

const (
	opusInputRate    = 16000
	opusFrameSamples = opusInputRate / 50 // 20 ms
	opusClockRate    = 48000
)

// EncodeWAVToOggOpus converts the application's PCM16 meeting mix to a
// standards-compliant Ogg/Opus file without invoking an external program.
// The current voice-oriented path uses Opus SILK wideband at 16 kHz and stores
// timestamps in the mandatory 48 kHz Ogg granule clock.
func EncodeWAVToOggOpus(inputPath, outputPath, meetingUID string, bitrateKbps int) error {
	return EncodeWAVToOggOpusContext(context.Background(), inputPath, outputPath, meetingUID, bitrateKbps)
}

// EncodeWAVToOggOpusContext is the cancellable variant used by the meeting
// pipeline. A canceled conversion leaves the source WAV untouched and removes
// the incomplete .part file.
func EncodeWAVToOggOpusContext(ctx context.Context, inputPath, outputPath, meetingUID string, bitrateKbps int) error {
	in, err := os.Open(inputPath)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := ReadWAVInfo(in)
	if err != nil {
		return err
	}
	if info.BitsPerSample != 16 || info.SampleRate != opusInputRate || (info.Channels != 1 && info.Channels != 2) {
		return fmt.Errorf("Opus encoder expects PCM16 WAV at 16000 Hz with 1 or 2 channels; got %d Hz, %d channel(s), %d bit", info.SampleRate, info.Channels, info.BitsPerSample)
	}
	if bitrateKbps < 6 || bitrateKbps > 256 {
		return errors.New("Opus bitrate must be between 6 and 256 kbit/s")
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return err
	}
	part := outputPath + ".part"
	_ = os.Remove(part)
	out, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		_ = out.Close()
		if !success {
			_ = os.Remove(part)
		}
	}()

	encoder, err := opus.NewEncoder(
		opus.WithChannels(1),
		opus.WithBitrate(bitrateKbps*1000),
		opus.WithApplication(opus.ApplicationVoIP),
		opus.WithComplexity(7),
		opus.WithVBR(true),
		opus.WithConstrainedVBR(false),
	)
	if err != nil {
		return err
	}
	var serialBytes [4]byte
	if _, err := rand.Read(serialBytes[:]); err != nil {
		return err
	}
	writer := oggOpusWriter{w: out, serial: binary.LittleEndian.Uint32(serialBytes[:])}
	if err := writer.writeHeader(info.Channels, info.SampleRate, meetingUID); err != nil {
		return err
	}

	reader := io.LimitReader(in, info.DataSize)
	frameBytes := opusFrameSamples * info.Channels * 2
	raw := make([]byte, frameBytes)
	pcm := make([]int16, opusFrameSamples)
	packet := make([]byte, 1500)
	var packets [][]byte
	var encodedSamples, originalSamples int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := io.ReadFull(reader, raw)
		if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
			return readErr
		}
		if n == 0 {
			break
		}
		frames := n / (info.Channels * 2)
		originalSamples += int64(frames)
		for i := 0; i < opusFrameSamples; i++ {
			if i >= frames {
				pcm[i] = 0
				continue
			}
			if info.Channels == 1 {
				pcm[i] = int16(binary.LittleEndian.Uint16(raw[i*2 : i*2+2]))
			} else {
				offset := i * 4
				left := int32(int16(binary.LittleEndian.Uint16(raw[offset : offset+2])))
				right := int32(int16(binary.LittleEndian.Uint16(raw[offset+2 : offset+4])))
				pcm[i] = int16((left + right) / 2)
			}
		}
		written, encodeErr := encoder.EncodeSILK(pcm, opus.BandwidthWideband, packet)
		if encodeErr != nil {
			return encodeErr
		}
		packets = append(packets, append([]byte(nil), packet[:written]...))
		encodedSamples += opusFrameSamples
		if len(packets) > 50 {
			if err := writer.writeAudioPage(packets[:50], uint64((encodedSamples-opusFrameSamples)*3), false); err != nil {
				return err
			}
			packets = packets[50:]
		}
		if readErr == io.ErrUnexpectedEOF {
			break
		}
	}
	if len(packets) == 0 {
		return errors.New("cannot encode empty WAV")
	}
	if err := writer.writeAudioPage(packets, uint64(originalSamples*3), true); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := replaceFile(part, outputPath); err != nil {
		return err
	}
	success = true
	return nil
}

type oggOpusWriter struct {
	w      io.Writer
	serial uint32
	seq    uint32
}

func (w *oggOpusWriter) writeHeader(_ int, inputRate int, meetingUID string) error {
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8] = 1
	head[9] = 1 // encoded stream is mono, including a stereo WAV downmix
	binary.LittleEndian.PutUint16(head[10:12], 0)
	binary.LittleEndian.PutUint32(head[12:16], uint32(inputRate))
	binary.LittleEndian.PutUint16(head[16:18], 0)
	head[18] = 0
	if err := w.writePage([][]byte{head}, 0, 0x02); err != nil {
		return err
	}
	vendor := []byte("LocalMeetAssist pure-Go Opus")
	comment := []byte("MEETING_UID=" + meetingUID)
	tags := make([]byte, 8+4+len(vendor)+4+4+len(comment))
	copy(tags, "OpusTags")
	offset := 8
	binary.LittleEndian.PutUint32(tags[offset:offset+4], uint32(len(vendor)))
	offset += 4
	copy(tags[offset:], vendor)
	offset += len(vendor)
	binary.LittleEndian.PutUint32(tags[offset:offset+4], 1)
	offset += 4
	binary.LittleEndian.PutUint32(tags[offset:offset+4], uint32(len(comment)))
	offset += 4
	copy(tags[offset:], comment)
	return w.writePage([][]byte{tags}, 0, 0)
}

func (w *oggOpusWriter) writeAudioPage(packets [][]byte, granule uint64, eos bool) error {
	headerType := byte(0)
	if eos {
		headerType = 0x04
	}
	return w.writePage(packets, granule, headerType)
}

func (w *oggOpusWriter) writePage(packets [][]byte, granule uint64, headerType byte) error {
	var lacing []byte
	var payload []byte
	for _, packet := range packets {
		remaining := len(packet)
		for remaining >= 255 {
			lacing = append(lacing, 255)
			remaining -= 255
		}
		lacing = append(lacing, byte(remaining))
		payload = append(payload, packet...)
	}
	if len(lacing) > 255 {
		return errors.New("too many Ogg lacing segments")
	}
	page := make([]byte, 27+len(lacing)+len(payload))
	copy(page, "OggS")
	page[4] = 0
	page[5] = headerType
	binary.LittleEndian.PutUint64(page[6:14], granule)
	binary.LittleEndian.PutUint32(page[14:18], w.serial)
	binary.LittleEndian.PutUint32(page[18:22], w.seq)
	page[26] = byte(len(lacing))
	copy(page[27:], lacing)
	copy(page[27+len(lacing):], payload)
	binary.LittleEndian.PutUint32(page[22:26], oggCRC(page))
	if _, err := w.w.Write(page); err != nil {
		return err
	}
	w.seq++
	return nil
}

func oggCRC(data []byte) uint32 {
	var crc uint32
	for _, value := range data {
		crc ^= uint32(value) << 24
		for range 8 {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func replaceFile(source, target string) error {
	backup := target + ".old"
	_ = os.Remove(backup)
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(source, target); err != nil {
		_ = os.Rename(backup, target)
		return err
	}
	_ = os.Remove(backup)
	return nil
}
