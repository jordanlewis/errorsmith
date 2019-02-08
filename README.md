errorsmith is a random fault injector for Go.

Run `errorsmith file.go [-o outfile]` to inject random errors into the Go program.

Errors are injected at sites where errors are propagated using the classic Go pattern:

```
if err != nil {
    return err
}
```

The program adds a code block above such statements, setting `err` to a new error
if a random coin flip lands.

The probability for error injection can be changed at codegen time with the `-error-percent` flag.
