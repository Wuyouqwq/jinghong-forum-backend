package app

import (
	"math"
	"testing"
	"time"
)

func TestHotDecayWeightUsesConfiguredHalfLife(t *testing.T) {
	gravity := 1.2
	halfLife := 72 * time.Hour
	for name, test := range map[string]struct {
		age  time.Duration
		want float64
	}{
		"new":       {age: 0, want: 1},
		"future":    {age: -time.Hour, want: 1},
		"half-life": {age: halfLife, want: 0.5},
	} {
		t.Run(name, func(t *testing.T) {
			if got := hotDecayWeight(test.age, halfLife, gravity); math.Abs(got-test.want) > 1e-12 {
				t.Fatalf("hotDecayWeight() = %.12f, want %.12f", got, test.want)
			}
		})
	}
	if got := hotDecayWeight(2*halfLife, halfLife, gravity); got >= 0.5 || got <= 0 {
		t.Fatalf("weight after two half-lives = %.6f, want between 0 and 0.5", got)
	}
}

func TestHotRankingScoreHumanSignals(t *testing.T) {
	params := hotRankParameters{Gravity: 1.2, BaseHalfLife: 72 * time.Hour, MomentumHalfLife: 24 * time.Hour}
	newPost := hotRankingScore(params, time.Hour, 0, 0, nil)
	oldPost := hotRankingScore(params, 7*24*time.Hour, 0, 0, nil)
	if newPost <= oldPost {
		t.Fatalf("new post score %.6f should exceed dormant old post %.6f", newPost, oldPost)
	}

	oneParticipant := hotRankingScore(params, 12*time.Hour, 0, 1, []time.Duration{time.Hour})
	threeParticipants := hotRankingScore(params, 12*time.Hour, 0, 3, []time.Duration{time.Hour, time.Hour, time.Hour})
	if threeParticipants <= oneParticipant {
		t.Fatalf("three-participant score %.6f should exceed one-participant score %.6f", threeParticipants, oneParticipant)
	}

	dormant := hotRankingScore(params, 30*24*time.Hour, 20, 10, nil)
	revived := hotRankingScore(params, 30*24*time.Hour, 20, 10, []time.Duration{time.Hour, time.Hour, 2 * time.Hour})
	if revived <= dormant {
		t.Fatalf("revived score %.6f should exceed dormant score %.6f", revived, dormant)
	}

	tenPeople := hotRankingScore(params, 0, 10, 0, nil)
	hundredPeople := hotRankingScore(params, 0, 100, 0, nil)
	if hundredPeople/tenPeople >= 2 {
		t.Fatalf("log compression ratio = %.6f, want less than 2", hundredPeople/tenPeople)
	}
}
