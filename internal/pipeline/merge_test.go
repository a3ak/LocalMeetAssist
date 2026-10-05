package pipeline

import (
	"testing"

	"localmeetassist/internal/model"
)

func TestDeduplicateEchoRemovesMatchingMicrophoneCopy(t *testing.T) {
	microphone := []model.Segment{
		{StartMS: 900, EndMS: 2600, Source: "microphone", Text: "Добрый день, коллеги"},
		{StartMS: 4000, EndMS: 5200, Source: "microphone", Text: "Я покажу экран"},
	}
	system := []model.Segment{
		{StartMS: 1000, EndMS: 2500, Source: "system", Text: "Добрый день коллеги"},
	}

	kept, dropped := deduplicateEcho(microphone, system, 1500, 0.72)
	if len(kept) != 1 || kept[0].Text != "Я покажу экран" {
		t.Fatalf("unexpected kept segments: %+v", kept)
	}
	if len(dropped) != 1 || dropped[0].Text != "Добрый день, коллеги" {
		t.Fatalf("unexpected dropped segments: %+v", dropped)
	}
}

func TestDeduplicateEchoKeepsDifferentSimultaneousSpeech(t *testing.T) {
	microphone := []model.Segment{{StartMS: 1000, EndMS: 2500, Source: "microphone", Text: "Я с этим не согласен"}}
	system := []model.Segment{{StartMS: 1000, EndMS: 2500, Source: "system", Text: "Перейдём к следующему вопросу"}}

	kept, dropped := deduplicateEcho(microphone, system, 1500, 0.72)
	if len(kept) != 1 || len(dropped) != 0 {
		t.Fatalf("different speech must be retained: kept=%+v dropped=%+v", kept, dropped)
	}
}

func TestDeduplicateEchoRemovesTruncatedMicrophoneCopy(t *testing.T) {
	microphone := []model.Segment{{StartMS: 1100, EndMS: 2600, Source: "microphone", Text: "обсудим сроки поставки оборудования"}}
	system := []model.Segment{{StartMS: 900, EndMS: 4200, Source: "system", Text: "теперь давайте обсудим сроки поставки оборудования на площадку"}}
	kept, dropped := deduplicateEcho(microphone, system, 1000, 0.72)
	if len(kept) != 0 || len(dropped) != 1 {
		t.Fatalf("contained acoustic copy must be removed: kept=%+v dropped=%+v", kept, dropped)
	}
}

func TestDeduplicateEchoComparesCombinedSystemSegments(t *testing.T) {
	microphone := []model.Segment{{StartMS: 1000, EndMS: 3800, Source: "microphone", Text: "перейдем к следующему вопросу повестки"}}
	system := []model.Segment{
		{StartMS: 900, EndMS: 2100, Source: "system", Text: "перейдем к следующему"},
		{StartMS: 2100, EndMS: 3900, Source: "system", Text: "вопросу повестки"},
	}
	kept, dropped := deduplicateEcho(microphone, system, 500, 0.72)
	if len(kept) != 0 || len(dropped) != 1 {
		t.Fatalf("copy split into system segments must be removed: kept=%+v dropped=%+v", kept, dropped)
	}
}

func TestAssignSystemSpeakersUsesLargestOverlap(t *testing.T) {
	segments := []model.Segment{
		{StartMS: 100, EndMS: 900, Source: "system", Text: "Первый"},
		{StartMS: 1200, EndMS: 1900, Source: "system", Text: "Второй"},
	}
	turns := []SpeakerTurn{
		{StartMS: 0, EndMS: 1000, SpeakerID: "SPEAKER_00"},
		{StartMS: 1000, EndMS: 2000, SpeakerID: "SPEAKER_01"},
	}

	got := assignSystemSpeakers(segments, turns)
	if got[0].SpeakerID != "SPEAKER_00" || got[1].SpeakerID != "SPEAKER_01" {
		t.Fatalf("unexpected speaker assignment: %+v", got)
	}
}

func TestTranscriptLooksDegenerate(t *testing.T) {
	segments := []model.Segment{
		{Text: "Корректор А. Егорова"},
		{Text: "Корректор А Егорова"},
		{Text: "Полезная фраза"},
		{Text: "Корректор А. Егорова"},
	}
	if !transcriptLooksDegenerate(segments) {
		t.Fatal("repeated hallucination should be detected")
	}
	if transcriptLooksDegenerate([]model.Segment{{Text: "один"}, {Text: "два"}, {Text: "три"}, {Text: "четыре"}}) {
		t.Fatal("different phrases must not be marked degenerate")
	}
}

func TestSuppressRepeatedHallucinationsKeepsTwoOccurrences(t *testing.T) {
	segments := []model.Segment{
		{StartMS: 0, Text: "Слышно?"},
		{StartMS: 2000, Text: "Слышно?"},
		{StartMS: 32000, Text: "Ещё раз привет"},
		{StartMS: 81000, Text: "Слышно?"},
		{StartMS: 137000, Text: "Слышно?"},
		{StartMS: 220000, Text: "Слышно?"},
	}
	filtered, dropped := suppressRepeatedHallucinations(segments, 2)
	if dropped != 3 || len(filtered) != 3 {
		t.Fatalf("unexpected filtering: dropped=%d segments=%+v", dropped, filtered)
	}
	if filtered[0].StartMS != 0 || filtered[1].StartMS != 2000 || filtered[2].Text != "Ещё раз привет" {
		t.Fatalf("legitimate first repetitions or unique phrase were lost: %+v", filtered)
	}
}

func TestSuppressRepeatedHallucinationsInsideLongTranscript(t *testing.T) {
	segments := make([]model.Segment, 0, 35)
	for index := 0; index < 25; index++ {
		segments = append(segments, model.Segment{StartMS: int64(index * 1000), Text: "Уникальная фраза " + string(rune('А'+index))})
	}
	for index := 0; index < 10; index++ {
		segments = append(segments, model.Segment{StartMS: int64(60000 + index*10000), Text: "Корректор субтитров"})
	}
	filtered, dropped := suppressRepeatedHallucinations(segments, 2)
	if dropped != 8 || len(filtered) != 27 {
		t.Fatalf("long transcript repeat was not suppressed: dropped=%d len=%d", dropped, len(filtered))
	}
}

func TestCollapseRepeatedTokenRunsInsideSegment(t *testing.T) {
	segments := []model.Segment{{Text: "В-у, у-у, у-у, у-у, у-у, у-у, затем нормальная речь"}}
	filtered, dropped := collapseRepeatedTokenRuns(segments, 2, 5)
	if dropped != 3 {
		t.Fatalf("unexpected dropped count: %d (%q)", dropped, filtered[0].Text)
	}
	if filtered[0].Text != "В-у, у-у, у-у, затем нормальная речь" {
		t.Fatalf("unexpected collapsed text: %q", filtered[0].Text)
	}
}

func TestCollapseRepeatedTokenRunsKeepsOrdinaryRepetition(t *testing.T) {
	segments := []model.Segment{{Text: "да, да, я согласен"}}
	filtered, dropped := collapseRepeatedTokenRuns(segments, 2, 5)
	if dropped != 0 || filtered[0].Text != segments[0].Text {
		t.Fatalf("ordinary repetition was changed: %+v dropped=%d", filtered, dropped)
	}
}

func TestSuppressDegenerateRunsDropsLongHallucinationTail(t *testing.T) {
	segments := []model.Segment{
		{Text: "Нормальная речь"},
		{Text: "у-у, у-у"},
		{Text: "С-у, у-у, у-у"},
		{Text: "Шоу, шоу, шоу"},
		{Text: "В-в-в-в-в-в"},
	}
	filtered, dropped := suppressDegenerateRuns(segments, 4)
	if dropped != 4 || len(filtered) != 1 || filtered[0].Text != "Нормальная речь" {
		t.Fatalf("hallucination tail was not removed: dropped=%d segments=%+v", dropped, filtered)
	}
}

func TestSuppressDegenerateRunsKeepsShortRealRepetition(t *testing.T) {
	segments := []model.Segment{{Text: "Да, да"}, {Text: "Да, да"}, {Text: "Продолжаем обсуждение"}}
	filtered, dropped := suppressDegenerateRuns(segments, 4)
	if dropped != 0 || len(filtered) != len(segments) {
		t.Fatalf("short repetition was removed: dropped=%d segments=%+v", dropped, filtered)
	}
}

func TestMarkMicrophoneOwner(t *testing.T) {
	mixed := []model.Segment{
		{StartMS: 0, EndMS: 1000, SpeakerID: "SPEAKER_00", Text: "Добрый день коллеги"},
		{StartMS: 2000, EndMS: 3000, SpeakerID: "SPEAKER_01", Text: "Начнем обсуждение"},
	}
	microphone := []model.Segment{{StartMS: 100, EndMS: 900, Text: "Добрый день, коллеги"}}
	markMicrophoneOwner(mixed, microphone, 500, 0.72)
	if mixed[0].SpeakerID != "microphone_owner" || mixed[0].Source != "microphone" || mixed[1].SpeakerID != "SPEAKER_01" {
		t.Fatalf("unexpected labels: %+v", mixed)
	}
}

func TestSelectSpeakerSamplesUsesLongestTurns(t *testing.T) {
	turns := []SpeakerTurn{
		{StartMS: 0, EndMS: 700, SpeakerID: "SPEAKER_00"},
		{StartMS: 1000, EndMS: 9000, SpeakerID: "SPEAKER_00"},
		{StartMS: 2000, EndMS: 13000, SpeakerID: "SPEAKER_01"},
	}
	samples := selectSpeakerSamples(turns, 1)
	if got := samples["SPEAKER_00"]; len(got) != 1 || got[0].StartMS != 1000 || got[0].EndMS != 9000 {
		t.Fatalf("unexpected SPEAKER_00 samples: %+v", got)
	}
	if got := samples["SPEAKER_01"]; len(got) != 1 || got[0].StartMS != 2000 || got[0].EndMS != 12000 {
		t.Fatalf("long sample must be capped at 10 seconds: %+v", got)
	}
}
