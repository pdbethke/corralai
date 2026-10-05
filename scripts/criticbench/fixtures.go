// SPDX-License-Identifier: Elastic-2.0

package main

// fixture is one suite with a KNOWN answer: which tests can never fail.
// The comparison grades each critic mode against this list, not merely
// against the other mode, so "both agree" cannot pass for "both are right".
type fixture struct {
	name, goal, codePath, code, testPath, tests string
	vacuous                                     []string // tests that can never fail
	all                                         []string // every test in the file
	// broken is the same package with the behavior wrecked. It is how the
	// answer key is CHECKED rather than asserted: against it, exactly the
	// vacuous tests still pass and every other test fails
	// (TestFixtureAnswerKeysAreExecuted).
	broken string
}

var fixtures = []fixture{
	{
		name: "wallet", goal: "a deposit must be positive and a withdrawal must never exceed the balance",
		codePath: "wallet/wallet.go", testPath: "wallet/wallet_test.go",
		code: `package wallet

import "errors"

type Wallet struct{ balance int }

func (w *Wallet) Deposit(n int) error {
	if n <= 0 {
		return errors.New("deposit must be positive")
	}
	w.balance += n
	return nil
}

func (w *Wallet) Withdraw(n int) error {
	if n > w.balance {
		return errors.New("insufficient funds")
	}
	w.balance -= n
	return nil
}

func (w *Wallet) Balance() int { return w.balance }
`,
		tests: `package wallet

import "testing"

func TestDepositAdds(t *testing.T) {
	var w Wallet
	if err := w.Deposit(5); err != nil || w.Balance() != 5 {
		t.Fatalf("balance %d err %v", w.Balance(), err)
	}
}

func TestWithdrawInsufficient(t *testing.T) {
	var w Wallet
	if err := w.Withdraw(1); err == nil {
		t.Fatal("withdrawing from an empty wallet must fail")
	}
}

func TestBalanceStartsZero(t *testing.T) {
	var w Wallet
	if w.Balance() != 0 {
		t.Fatal("a new wallet must be empty")
	}
}

func TestDepositRuns(t *testing.T) {
	var w Wallet
	_ = w.Deposit(10)
}

func TestWithdrawTautology(t *testing.T) {
	var w Wallet
	_ = w.Deposit(3)
	_ = w.Withdraw(2)
	if w.Balance() != w.Balance() {
		t.Fatal("balance changed")
	}
}
`,
		broken: `package wallet

type Wallet struct{ balance int }

func (w *Wallet) Deposit(n int) error  { return nil }
func (w *Wallet) Withdraw(n int) error { return nil }
func (w *Wallet) Balance() int         { return 7 }
`,
		vacuous: []string{"TestDepositRuns", "TestWithdrawTautology"},
		all:     []string{"TestDepositAdds", "TestWithdrawInsufficient", "TestBalanceStartsZero", "TestDepositRuns", "TestWithdrawTautology"},
	},
	{
		name: "parse", goal: "ParsePort accepts 1..65535 and rejects everything else",
		codePath: "netx/port.go", testPath: "netx/port_test.go",
		code: `package netx

import (
	"fmt"
	"strconv"
)

func ParsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("not a number: %w", err)
	}
	if n < 1 || n > 65535 {
		return 0, fmt.Errorf("port %d out of range", n)
	}
	return n, nil
}
`,
		tests: `package netx

import "testing"

func TestParsePortValid(t *testing.T) {
	if n, err := ParsePort("8080"); err != nil || n != 8080 {
		t.Fatalf("got %d %v", n, err)
	}
}

func TestParsePortZero(t *testing.T) {
	if _, err := ParsePort("0"); err == nil {
		t.Fatal("0 is not a port")
	}
}

func TestParsePortTooHigh(t *testing.T) {
	if _, err := ParsePort("65536"); err == nil {
		t.Fatal("65536 is out of range")
	}
}

func TestParsePortMax(t *testing.T) {
	if n, err := ParsePort("65535"); err != nil || n != 65535 {
		t.Fatalf("got %d %v", n, err)
	}
}

func TestParsePortNotNumber(t *testing.T) {
	if _, err := ParsePort("http"); err == nil {
		t.Fatal("letters are not a port")
	}
}

func TestParsePortNegative(t *testing.T) {
	if _, err := ParsePort("-1"); err == nil {
		t.Fatal("negative is not a port")
	}
}
`,
		broken: `package netx

func ParsePort(s string) (int, error) { return 0, nil }
`,
		vacuous: nil,
		all:     []string{"TestParsePortValid", "TestParsePortZero", "TestParsePortTooHigh", "TestParsePortMax", "TestParsePortNotNumber", "TestParsePortNegative"},
	},
	{
		name: "rates", goal: "Fee charges 2% of the amount, at least 1 and at most 50",
		codePath: "billing/fee.go", testPath: "billing/fee_test.go",
		code: `package billing

func Fee(amount int) int {
	f := amount * 2 / 100
	if f < 1 {
		return 1
	}
	if f > 50 {
		return 50
	}
	return f
}
`,
		tests: `package billing

import "testing"

func TestFeeTypical(t *testing.T) {
	if got := Fee(1000); got != 20 {
		t.Fatalf("Fee(1000) = %d, want 20", got)
	}
}

func TestFeeCap(t *testing.T) {
	if got := Fee(100000); got != 50 {
		t.Fatalf("Fee(100000) = %d, want 50", got)
	}
}

func TestFeeNoAssert(t *testing.T) {
	Fee(500)
}

func TestFeeSelfCompare(t *testing.T) {
	if Fee(10) != Fee(10) {
		t.Fatal("not deterministic")
	}
}

func TestFeeLogsOnly(t *testing.T) {
	t.Logf("fee is %d", Fee(250))
}

func TestFeeBlank(t *testing.T) {
	_ = Fee(99)
}

func TestFeeTautology(t *testing.T) {
	got := Fee(3000)
	if got != got {
		t.Fatal("impossible")
	}
}

func TestFeeDeadBranch(t *testing.T) {
	if false {
		t.Fatal(Fee(1))
	}
}

func TestFeeIgnoresResult(t *testing.T) {
	for _, a := range []int{1, 2, 3} {
		Fee(a)
	}
}
`,
		broken: `package billing

func Fee(amount int) int { return 0 }
`,
		vacuous: []string{"TestFeeNoAssert", "TestFeeSelfCompare", "TestFeeLogsOnly", "TestFeeBlank", "TestFeeTautology", "TestFeeDeadBranch", "TestFeeIgnoresResult"},
		all:     []string{"TestFeeTypical", "TestFeeCap", "TestFeeNoAssert", "TestFeeSelfCompare", "TestFeeLogsOnly", "TestFeeBlank", "TestFeeTautology", "TestFeeDeadBranch", "TestFeeIgnoresResult"},
	},
}
