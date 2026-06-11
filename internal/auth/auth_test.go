package auth

import "testing"

func TestCheckLogin_CorrectCredentials(t *testing.T) {
	a := Auth{User: "admin", Password: "s3cret", Token: "TOK"}
	tok, ok := a.CheckLogin("admin", "s3cret")
	if !ok {
		t.Fatal("want ok")
	}
	if tok != "TOK" {
		t.Fatalf("token = %q, want TOK", tok)
	}
}

func TestCheckLogin_WrongPassword(t *testing.T) {
	a := Auth{User: "admin", Password: "s3cret", Token: "TOK"}
	if _, ok := a.CheckLogin("admin", "wrong"); ok {
		t.Fatal("want !ok for wrong password")
	}
}

func TestCheckLogin_WrongUser(t *testing.T) {
	a := Auth{User: "admin", Password: "s3cret", Token: "TOK"}
	if _, ok := a.CheckLogin("guest", "s3cret"); ok {
		t.Fatal("want !ok for wrong user")
	}
}

func TestVerifyToken(t *testing.T) {
	a := Auth{Token: "TOK"}
	if !a.VerifyToken("TOK") {
		t.Fatal("want valid")
	}
	if a.VerifyToken("WRONG") {
		t.Fatal("want invalid")
	}
	if a.VerifyToken("") {
		t.Fatal("want empty rejected")
	}
}
