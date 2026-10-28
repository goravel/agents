---
name: Goravel Testing
description: >
  Goravel test-writing and test-running conventions. Use this skill when adding,
  updating, reviewing, or running tests in Goravel repositories.
---

# Goravel Testing

- Don't run `go test ./...` unless the user asks for all tests. Prefer `go test <pkg>` or `go test <pkg> -run <TestName>`.
- Use table-driven tests with `t.Run` sub-tests: one row per case, covering happy path, failure, and edge cases. Never one test function per case.
- Name tests `Test<FunctionName>_<Optional>`.
- Skip trivial getters/setters unless they hold non-trivial logic.
- Assert with `testify`: `assert.Equal(t, want, got)` or `require.NoError(t, err)` — not `assert.New(t)`, and never inside an `if`; the assertion reports for you.
- Inside a `testify/suite`, assert with the suite methods, which take no `t`: `s.Equal(want, got)`, `s.Require().NoError(err)`.
- Compare whole values — full maps, structs, slices — not field by field. For errors use `assert.Equal(t, assert.AnError, err)` or `assert.ErrorIs`.
- Mocks: call `EXPECT()`; every expectation uses `.Once()` or `.Times(n)`, `.Maybe()` only when needed; avoid `mock.Anything` where a concrete value works, and `mock.MatchedBy` unless the argument is generated at runtime.
- Keep the setup minimal: a `beforeEach` closure is enough; add a helper only when more than one test uses it.
- The basic pattern:

```go
func TestUserService_Store(t *testing.T) {
	tests := []struct {
		name        string
		input       models.User
		want        uint
		expectError error
	}{
		{name: "saves a valid user", input: models.User{Name: "Bowen"}, want: 1},
		{name: "returns the repository error", input: models.User{}, expectError: assert.AnError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := userService.Store(tt.input)
			assert.Equal(t, tt.expectError, err)
			assert.Equal(t, tt.want, id)
		})
	}
}
```
