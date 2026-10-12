package claude

import "testing"

func TestForwardedLoginProof(t *testing.T) {
	proof, err := selectLoginProof([]Mail{{ID: 12, Subject: "FW: Your Claude verification code", Text: "Forwarded message for person@hotmail.com\nYour code: 123456"}}, 11)
	if err != nil || proof == nil || proof.Code != "123456" {
		t.Fatalf("forwarded code not recognized: %+v %v", proof, err)
	}
	proof, err = selectLoginProof([]Mail{{ID: 11, Subject: "FW: Your Claude verification code", Text: "Your code: 123456"}}, 11)
	if err != nil || proof != nil {
		t.Fatal("old forwarded email accepted")
	}
}
