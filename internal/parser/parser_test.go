package parser

import (
	"testing"
)

func TestParsePython(t *testing.T) {
	src := []byte(`import os

def authenticate(user, password):
    return user

class Token:
    def refresh(self):
        pass

@decorator
def wrapped():
    pass
`)
	fd, err := Parse(Python, "auth.py", src)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, s := range fd.Symbols {
		names[s.Name] = s.Kind
	}
	if names["authenticate"] != "function" {
		t.Errorf("expected authenticate function, got %v", names)
	}
	if names["Token"] != "class" {
		t.Errorf("expected Token class, got %v", names)
	}
	if names["refresh"] != "function" {
		t.Errorf("expected refresh function, got %v", names)
	}
	if names["wrapped"] != "function" {
		t.Errorf("expected wrapped function (decorated), got %v", names)
	}
}

func TestParseGo(t *testing.T) {
	src := []byte(`package main

type User struct {
	Name string
}

func (u *User) Greet() string {
	return "hi " + u.Name
}

func main() {}
`)
	fd, err := Parse(Go, "main.go", src)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, s := range fd.Symbols {
		names[s.Name] = s.Kind
	}
	if names["User"] != "type" {
		t.Errorf("expected User type, got %v", names)
	}
	if names["Greet"] != "method" {
		t.Errorf("expected Greet method, got %v", names)
	}
	if names["main"] != "function" {
		t.Errorf("expected main function, got %v", names)
	}
}

func TestParseTypeScript(t *testing.T) {
	src := []byte(`export interface User {
  name: string;
}

export class Auth {
  login(user: string): boolean {
    return true;
  }
}

export function helper() {}

const topLevel = 42;
`)
	fd, err := Parse(TypeScript, "auth.ts", src)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, s := range fd.Symbols {
		names[s.Name] = s.Kind
	}
	if names["User"] != "interface" {
		t.Errorf("expected User interface, got %v", names)
	}
	if names["Auth"] != "class" {
		t.Errorf("expected Auth class, got %v", names)
	}
	if names["login"] != "method" {
		t.Errorf("expected login method, got %v", names)
	}
	if names["helper"] != "function" {
		t.Errorf("expected helper function, got %v", names)
	}
	if names["topLevel"] != "variable" {
		t.Errorf("expected topLevel variable, got %v", names)
	}
}

func TestParseHCL(t *testing.T) {
	src := []byte(`resource "aws_instance" "web" {
  ami = "ami-123"
}

variable "region" {
  default = "us-east-1"
}
`)
	fd, err := Parse(HCL, "main.tf", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(fd.Symbols) == 0 {
		t.Fatal("expected HCL symbols")
	}
	if fd.Symbols[0].Name != "resource" {
		t.Errorf("expected resource block, got %s", fd.Symbols[0].Name)
	}
}

func TestDetectLanguage(t *testing.T) {
	cases := map[string]Language{
		"foo.py":    Python,
		"foo.ts":    TypeScript,
		"foo.tsx":   TypeScript,
		"foo.js":    JavaScript,
		"foo.jsx":   JavaScript,
		"main.go":   Go,
		"main.tf":   HCL,
		"vars.tfvars": HCL,
	}
	for path, want := range cases {
		got, ok := DetectLanguage(path)
		if !ok || got != want {
			t.Errorf("DetectLanguage(%s) = %v, %v; want %v", path, got, ok, want)
		}
	}
	if _, ok := DetectLanguage("foo.txt"); ok {
		t.Error("expected foo.txt to be unsupported")
	}
}
