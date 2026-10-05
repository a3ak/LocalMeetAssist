package server

import (
	"testing"
	"time"

	"localmeetassist/internal/model"
)

func TestMeetingSearchStructuredQuery(t *testing.T) {
	started := time.Date(2026, 9, 20, 12, 0, 0, 0, time.Local)
	meeting := model.Meeting{
		Title:      "Итоги недели команды",
		StartedAt:  started,
		Transcript: "Обсудили мониторинг инфраструктуры и дальнейшие задачи.",
		Summary:    "Решения по платформе наблюдаемости.",
	}
	speakers := []model.Speaker{{DisplayName: "Спикер1"}, {DisplayName: "Спикер2"}}
	query := "Speakers: Спикер1, Спикер2, !Спикер3; Meeting: Итоги недели; Discussion: Мониторинг инфраструктуры; Date: after 20.09.2026, before 30.09.2026"
	clauses, err := parseMeetingSearch(query)
	if err != nil {
		t.Fatal(err)
	}
	if !matchesMeetingSearch(meeting, speakers, clauses) {
		t.Fatal("meeting must match the structured query")
	}
	speakers = append(speakers, model.Speaker{DisplayName: "Спикер3"})
	if matchesMeetingSearch(meeting, speakers, clauses) {
		t.Fatal("negative speaker must exclude the meeting")
	}
}

func TestMeetingSearchFreeTextIsWholePhrase(t *testing.T) {
	meeting := model.Meeting{Title: "Мониторинг других систем", Transcript: "Развитие инфраструктуры"}
	clauses, err := parseMeetingSearch("Мониторинг инфраструктуры")
	if err != nil {
		t.Fatal(err)
	}
	if matchesMeetingSearch(meeting, nil, clauses) {
		t.Fatal("separate words in different fields must not match a whole phrase")
	}
	meeting.Transcript = "Обсудили мониторинг инфраструктуры"
	if !matchesMeetingSearch(meeting, nil, clauses) {
		t.Fatal("whole phrase must match")
	}
}

func TestMeetingSearchDateBoundsAreInclusive(t *testing.T) {
	clauses, err := parseMeetingSearch("Date: after 20.09.2026, before 20.09.2026")
	if err != nil {
		t.Fatal(err)
	}
	for _, hour := range []int{0, 23} {
		meeting := model.Meeting{StartedAt: time.Date(2026, 9, 20, hour, 59*(hour/23), 0, 0, time.Local)}
		if !matchesMeetingSearch(meeting, nil, clauses) {
			t.Fatalf("hour %d must be inside the inclusive day", hour)
		}
	}
}
