# morestringer - more than Stringer

morestringer is an extention of Go's [Stringer](https://pkg.go.dev/golang.org/x/tools/cmd/stringer).

## Description

**morestringer** a tool to automate the creation of methods that
satisfy the `fmt.Stringer` interface. Given the name of a (signed or unsigned)
integer type `T` that has constants defined, stringer will create a new self-contained Go
source file implementing:

```go
func (t T) String() string
```

The file is created in the same package and directory as the package that defines `T`.
It has helpful defaults designed for use with go generate.

Stringer works best with constants that are consecutive values such as created using _`iota`_,
but creates good code regardless. In the future it might also provide custom support for
constant sets that are bit patterns.

For example, given this snippet,

```go
package painkiller

type Pill int

const (
    Placebo Pill = iota
    Aspirin
    Ibuprofen
    Paracetamol
    Acetaminophen = Paracetamol
)
```

running this command

```sh
morestringer -type=Pill
```

in the same directory will create the file pill_string.go, in package painkiller,
containing a definition of

```go
func (Pill) String() string
```

That method will translate the value of a Pill constant to the string representation
of the respective constant name, so that the call `fmt.Print(painkiller.Aspirin)` will
print the string `"Aspirin"`.

Typically this process would be run using go generate, like this:

```go
//go:generate stringer -type=Pill
```

If multiple constants have the same value, the lexically first matching name will
be used (in the example, Acetaminophen will print as "Paracetamol").

With no arguments, it processes the package in the current directory.
Otherwise, the arguments must name a single directory holding a Go package
or a set of Go source files that represent a single Go package.

The `-type` flag accepts a comma-separated list of types so a single run can
generate methods for multiple types. The default output file is t_string.go,
where t is the lower-cased name of the first type listed. It can be overridden
with the `-output` flag.

Types can also be declared in tests, in which case type declarations in the
non-test package or its test variant are preferred over types defined in the
package with suffix "_test".
The default output file for type declarations in tests is t_string_test.go with t picked as above.

The `-linecomment` flag tells stringer to generate the text of any line comment, trimmed
of leading spaces, instead of the constant name. For instance, if the constants above had a
Pill prefix, one could write

```go
PillAspirin // Aspirin
```

to suppress it in the output.

The `-trimprefix` flag specifies a prefix to remove from the constant names
when generating the string representations. For instance, `-trimprefix=Pill`
would be an alternative way to ensure that `PillAspirin.String() == "Aspirin"`.

## New in morestringer

If create binding code to a native C-library you might write something like that:

```go
package input

type Key uint16

const (
	Key1          Key = C.KEY_1
	Key2          Key = C.KEY_2
	Key3          Key = C.KEY_3
	Key4          Key = C.KEY_4
	Key5          Key = C.KEY_5
	Key6          Key = C.KEY_6
	Key7          Key = C.KEY_7
	Key8          Key = C.KEY_8
	Key9          Key = C.KEY_9
	Key0          Key = C.KEY_0
	KeyMinus      Key = C.KEY_MINUS
	KeyEqual      Key = C.KEY_EQUAL
	KeyBackspace  Key = C.KEY_BACKSPACE
	KeyTab        Key = C.KEY_TAB
	KeyQ          Key = C.KEY_Q
	KeyW          Key = C.KEY_W
	KeyE          Key = C.KEY_E
	KeyR          Key = C.KEY_R
	KeyT          Key = C.KEY_T
    ...
)
```

As those constants often are more known, morestringer adds option `-cnames` which assigns the C-name to
the constant rather than the constants name. `-linecomment` does override this option! Enabled the code produces:
`KeyQ.String() == "KEY_Q"`.

As an extension morestringer can generate lookup functions that take the constant name and returns the corresponding value if exists.
To generate such function, use `-lookup name`. `name` is the function name where `{}` is replaced with the actual type.
Generating a lookup using `-lookup {}ByName` enables following function:

```go
func KeyByName(name string) (Key, bool)
```

# Licensing

This project is licensed under the [BSD-3-Clause-License](./LICENSE).

