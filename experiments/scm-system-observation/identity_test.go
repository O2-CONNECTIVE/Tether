package main

import (
	"testing"
)

func TestObservedIdentityRequiresSystemPrimaryAndExactEnabledServiceSID(t *testing.T) {
	good := TokenObservation{User: Group{SID: "S-1-5-18"}, Type: 1, ExpectedServiceSID: "S-1-5-80-1-2-3-4-5", Groups: []Group{{SID: "S-1-5-80-1-2-3-4-5", Attributes: 4}}}
	if f := ValidateObservedSubject(good); len(f) != 0 {
		t.Fatal("worked SYSTEM observation rejected", f)
	}
	for _, change := range []func(*TokenObservation){func(v *TokenObservation) { v.User.SID = "S-1-5-19" }, func(v *TokenObservation) { v.Type = 2 }, func(v *TokenObservation) { v.ExpectedServiceSID = "S-1-5-80-9-8-7-6-5" }, func(v *TokenObservation) { v.ExpectedServiceSID = "S-1-5-80-01-2-3-4-5" }, func(v *TokenObservation) { v.Groups = nil; v.ServiceSIDPresentEnabled = true }, func(v *TokenObservation) { v.Groups = []Group{{SID: v.ExpectedServiceSID, Attributes: 2}} }, func(v *TokenObservation) { v.Groups = []Group{{SID: v.ExpectedServiceSID, Attributes: 20}} }} {
		v := good
		change(&v)
		if f := ValidateObservedSubject(v); len(f) == 0 {
			t.Fatalf("invalid observation accepted: %+v", v)
		}
	}
}
