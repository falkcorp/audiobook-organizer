<!-- file: docs/CODING_STANDARDS.md -->
<!-- version: 1.1.1 -->
<!-- guid: c1d2e3f4-a5b6-7c8d-9e0f-1a2b3c4d5e6f -->
<!-- last-edited: 2026-08-11 -->

# Coding Standards for Refactoring Task

**Purpose**: Complete coding standards and style guides for Go and TypeScript
used in this project.

---

## Table of Contents

1. [Go Coding Standards](#go-coding-standards)
2. [TypeScript Coding Standards](#typescript-coding-standards)
3. [Project-Specific Rules](#project-specific-rules)

---

<!-- file: .github/instructions/go.instructions.md -->
<!-- version: 1.6.1 -->
<!-- guid: 4f5a6b7c-8d9e-0f1a-2b3c-4d5e6f7a8b9c -->
<!-- last-edited: 2026-01-19 -->
<!-- DO NOT EDIT: This file is managed centrally in ghcommon repository -->
<!-- To update: Create an issue/PR in jdfalk/ghcommon -->

<!-- prettier-ignore-start -->
<!-- markdownlint-disable -->
---
applyTo: "**/*.go"
description: |
  Go language-specific coding, documentation, and testing rules for Copilot/AI agents and VS Code Copilot customization. These rules extend the general instructions in `general-coding.instructions.md` and merge all unique content from the Google Go Style Guide.
---
<!-- markdownlint-enable -->
<!-- prettier-ignore-end -->

# Go Coding Instructions

- Follow the [general coding instructions](general-coding.instructions.md).
- Follow the
  [Google Go Style Guide](https://google.github.io/styleguide/go/index.html) for
  additional best practices.
- All Go files must begin with the required file header (see general
  instructions for details and Go example).

## Core Principles

- Clarity over cleverness: Code should be clear and readable
- Simplicity: Prefer simple solutions over complex ones
- Consistency: Follow established patterns within the codebase
- Readability: Code is written for humans to read

## Version Requirements

- **MANDATORY**: All Go projects must use Go 1.23.0 or higher
- **NO EXCEPTIONS**: Do not use older Go versions in any repository
- Update `go.mod` files to specify `go 1.23` minimum version
- Update `go.work` files to specify `go 1.23` minimum version
- All Go file headers must use version 1.23.0 or higher
- Use `go version` to verify your installation meets requirements

## Naming Conventions

- Use short, concise, evocative package names (lowercase, no underscores)
- Use camelCase for unexported names, PascalCase for exported names
- Use short names for short-lived variables, descriptive names for longer-lived
  variables
- Use PascalCase for exported constants, camelCase for unexported constants
- Single-method interfaces should end in "-er" (e.g., Reader, Writer)

## Code Organization

- Use `goimports` to format imports automatically
- Group imports: standard library, third-party, local
- No blank lines within groups, one blank line between groups
- Keep functions short and focused
- Use blank lines to separate logical sections
- Order: receiver, name, parameters, return values

## Formatting

- Use tabs for indentation, spaces for alignment
- Opening brace on same line as declaration, closing brace on its own line
- No strict line length limit, but aim for readability

## Comments

- Every package should have a package comment
- Public functions must have comments starting with the function name
- Comment exported variables, explain purpose and constraints

## Error Handling

- Use lowercase for error messages, no punctuation at end
- Be specific about what failed
- Create custom error types for specific error conditions
- Use `errors.Is` and `errors.As` for error checking

## Best Practices

- Use short variable declarations (`:=`) when possible
- Use `var` for zero values or when type is important
- Use `make()` for slices and maps with known capacity
- Accept interfaces, return concrete types
- Keep interfaces small and focused
- Use channels for communication between goroutines
- Use sync primitives for protecting shared state
- Test file names end with `_test.go`, test function names start with `Test`
- Use table-driven tests for multiple scenarios

## Required File Header

All Go files must begin with a standard header as described in the
[general coding instructions](general-coding.instructions.md). Example for Go:

```go
// file: path/to/file.go
// version: 1.0.0
// guid: 123e4567-e89b-12d3-a456-426614174000
```

## Google Go Style Guide (Complete)

Follow the complete Google Go Style Guide below for all Go code:

### Google Go Style Guide (Complete)

This style guide provides comprehensive conventions for writing clean, readable,
and maintainable Go code.

#### Formatting

**gofmt:** All Go code must be formatted with `gofmt`. This is non-negotiable.

**Line Length:** No hard limit, but prefer shorter lines. Break long lines
sensibly.

**Indentation:** Use tabs for indentation (handled automatically by gofmt).

**Spacing:** Let gofmt handle spacing. Generally:

- No space inside parentheses: `f(a, b)`
- Space around binary operators: `a + b`
- No space around unary operators: `!condition`

#### Naming Conventions

**Packages:**

- Short, concise, evocative names
- Lowercase, no underscores or mixedCaps
- Often single words

```go
// Good
package user
package httputil
package json

// Bad
package userService
package http_util
```

**Interfaces:**

- Use -er suffix for single-method interfaces
- Use MixedCaps

```go
// Good
type Reader interface {
    Read([]byte) (int, error)
}

type FileWriter interface {
    WriteFile(string, []byte) error
}

// Bad
type IReader interface {  // Don't prefix with I
    Read([]byte) (int, error)
}
```

**Functions and Methods:**

- Use MixedCaps
- Exported functions start with capital letter
- Unexported functions start with lowercase letter

```go
// Good - exported
func CalculateTotal(price, tax float64) float64 {
    return price + tax
}

// Good - unexported
func validateInput(input string) bool {
    return len(input) > 0
}
```

**Variables:**

- Use MixedCaps
- Short names for short scopes
- Longer descriptive names for longer scopes

```go
// Good - short scope
for i, v := range items {
    process(i, v)
}

// Good - longer scope
func processUserData(userData map[string]interface{}) error {
    userID, exists := userData["id"]
    if !exists {
        return errors.New("user ID not found")
    }
    // ... more processing
}

// Bad
func processUserData(d map[string]interface{}) error {  // 'd' too short for scope
    userIdentificationNumber, exists := d["id"]  // Too long for simple value
    // ...
}
```

**Constants:**

- Use MixedCaps
- Group related constants in blocks

```go
// Good
const (
    StatusOK       = 200
    StatusNotFound = 404
    StatusError    = 500
)

const DefaultTimeout = 30 * time.Second

// Bad
const STATUS_OK = 200  // Don't use underscores
```

#### Package Organization

**Package Names:**

- Choose package names that are both short and clear
- Avoid generic names like "util", "common", "misc"
- Package name should describe what it provides, not what it contains

```go
// Good
package user     // for user management
package auth     // for authentication
package httputil // for HTTP utilities

// Bad
package utils    // Too generic
package stuff    // Too vague
```

**Import Organization:**

- Group imports: standard library, third-party, local
- Use goimports to handle this automatically

```go
import (
    // Standard library
    "fmt"
    "os"
    "time"

    // Third-party
    "github.com/gorilla/mux"
    "google.golang.org/grpc"

    // Local
    "myproject/internal/auth"
    "myproject/pkg/utils"
)
```

#### Error Handling

**Error Strings:**

- Don't capitalize error messages
- Don't end with punctuation
- Be descriptive but concise

```go
// Good
return fmt.Errorf("failed to connect to database: %w", err)
return errors.New("invalid user ID")

// Bad
return errors.New("Failed to connect to database.")  // Capitalized, punctuation
return errors.New("error")  // Too vague
```

**Error Wrapping:**

- Use fmt.Errorf with %w verb to wrap errors
- Add context to errors as they bubble up

```go
func processUser(id string) error {
    user, err := getUserFromDB(id)
    if err != nil {
        return fmt.Errorf("failed to get user %s: %w", id, err)
    }

    if err := validateUser(user); err != nil {
        return fmt.Errorf("user validation failed: %w", err)
    }

    return nil
}
```

**Error Checking:**

- Check errors immediately after operations
- Don't ignore errors (use \_ only when truly appropriate)

```go
// Good
file, err := os.Open(filename)
if err != nil {
    return fmt.Errorf("failed to open file: %w", err)
}
defer file.Close()

// Bad
file, _ := os.Open(filename)  // Ignoring error
// ... later in code ...
if file == nil {  // Too late to handle properly
    return errors.New("file is nil")
}
```

#### Function Design

**Function Length:** Keep functions short and focused. If a function is very
long, consider breaking it up.

**Function Signature:**

- Related parameters should be grouped
- Use meaningful parameter names

```go
// Good
func CreateUser(firstName, lastName, email string, age int) *User {
    return &User{
        FirstName: firstName,
        LastName:  lastName,
        Email:     email,
        Age:       age,
    }
}

// Bad
func CreateUser(a, b, c string, d int) *User {  // Unclear parameter names
    return &User{
        FirstName: a,
        LastName:  b,
        Email:     c,
        Age:       d,
    }
}
```

**Return Values:**

- Return errors as the last value
- Use named return parameters sparingly

```go
// Good
func divide(a, b float64) (float64, error) {
    if b == 0 {
        return 0, errors.New("division by zero")
    }
    return a / b, nil
}

// Acceptable for short, clear functions
func split(path string) (dir, file string) {
    // ... implementation
    return
}
```

#### Struct Design

**Field Organization:**

- Group related fields together
- Consider field alignment for memory efficiency

```go
type User struct {
    // Identity fields
    ID       int64
    Username string
    Email    string

    // Personal information
    FirstName string
    LastName  string
    Age       int

    // Metadata
    CreatedAt time.Time
    UpdatedAt time.Time
    Active    bool
}
```

**Constructor Functions:**

- Use New prefix for constructor functions
- Return pointers for structs that will be modified

```go
func NewUser(username, email string) *User {
    return &User{
        Username:  username,
        Email:     email,
        CreatedAt: time.Now(),
        Active:    true,
    }
}
```

#### Concurrency

**Goroutines:**

- Use goroutines for independent tasks
- Always consider how goroutines will exit

```go
// Good
func processItems(items []Item) {
    var wg sync.WaitGroup

    for _, item := range items {
        wg.Add(1)
        go func(item Item) {
            defer wg.Done()
            process(item)
        }(item)
    }

    wg.Wait()
}
```

**Channels:**

- Use channels for communication between goroutines
- Close channels when done sending

```go
func producer(ch chan<- int) {
    defer close(ch)
    for i := 0; i < 10; i++ {
        ch <- i
    }
}

func consumer(ch <-chan int) {
    for value := range ch {
        fmt.Println(value)
    }
}
```

#### Comments and Documentation

**Package Comments:**

- Every package should have a package comment
- Use complete sentences

```go
// Package user provides functionality for user management,
// including authentication, authorization, and user data operations.
package user
```

**Function Comments:**

- Document all exported functions
- Start with the function name
- Explain what the function does, not how

```go
// CalculateTotal computes the total price including tax.
// It returns an error if the tax rate is negative.
func CalculateTotal(price, taxRate float64) (float64, error) {
    if taxRate < 0 {
        return 0, errors.New("tax rate cannot be negative")
    }
    return price * (1 + taxRate), nil
}
```

**Inline Comments:**

- Use for complex logic or non-obvious code
- Explain why, not what

```go
// Sort items by priority to ensure high-priority items are processed first
sort.Slice(items, func(i, j int) bool {
    return items[i].Priority > items[j].Priority
})
```

#### Testing

**Test Functions:**

- Use TestXxx naming convention
- Use t.Run for subtests

```go
func TestCalculateTotal(t *testing.T) {
    tests := []struct {
        name     string
        price    float64
        taxRate  float64
        expected float64
        hasError bool
    }{
        {
            name:     "positive values",
            price:    100.0,
            taxRate:  0.1,
            expected: 110.0,
            hasError: false,
        },
        {
            name:     "negative tax rate",
            price:    100.0,
            taxRate:  -0.1,
            expected: 0.0,
            hasError: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result, err := CalculateTotal(tt.price, tt.taxRate)

            if tt.hasError {
                if err == nil {
                    t.Errorf("expected error, got none")
                }
                return
            }

            if err != nil {
                t.Errorf("unexpected error: %v", err)
                return
            }

            if result != tt.expected {
                t.Errorf("expected %f, got %f", tt.expected, result)
            }
        })
    }
}
```

**Benchmark Functions:**

```go
func BenchmarkCalculateTotal(b *testing.B) {
    for i := 0; i < b.N; i++ {
        CalculateTotal(100.0, 0.1)
    }
}
```

**Test Doubles — Prefer Narrow Hand-Written Fakes for New Code:**

When writing tests for new interfaces, prefer a narrow hand-written fake over a
generated mock (mockery). Generated mocks are appropriate for large or frequently-
changing interfaces where the maintenance cost of hand-writing outweighs the
drawback of generated boilerplate; for new, purpose-built interfaces, a small fake
that implements only the methods under test is more readable and less brittle.

```go
// Preferred for new interfaces: narrow fake, implements only what the test needs.
type fakeBookStore struct {
    books map[string]*database.Book
}

func (f *fakeBookStore) GetBookByID(id string) (*database.Book, error) {
    b, ok := f.books[id]
    if !ok {
        return nil, fmt.Errorf("not found: %s", id)
    }
    return b, nil
}
```

Use generated mocks (mockery) for existing large store or service interfaces
where the interface surface exceeds ~5 methods and is expected to grow. Document
the choice with a comment when the decision is non-obvious.

This covers the essential Go style guidelines including formatting, naming
conventions, package organization, error handling, function design, struct
design, concurrency, comments, and testing best practices.

---

<!-- file: .github/instructions/typescript.instructions.md -->
<!-- version: 1.2.1 -->
<!-- guid: ts123456-e89b-12d3-a456-426614174000 -->
<!-- last-edited: 2026-01-19 -->
<!-- DO NOT EDIT: This file is managed centrally in ghcommon repository -->
<!-- To update: Create an issue/PR in jdfalk/ghcommon -->

<!-- prettier-ignore-start -->
<!-- markdownlint-disable -->
---
applyTo: "**/*.ts"
description: |
  TypeScript language-specific coding, documentation, and testing rules for Copilot/AI agents and VS Code Copilot customization. These rules extend the general instructions in `general-coding.instructions.md` and merge all unique content from the Google TypeScript Style Guide.
---
<!-- markdownlint-enable -->
<!-- prettier-ignore-end -->

# TypeScript Coding Instructions

- Follow the [general coding instructions](general-coding.instructions.md).
- Follow the
  [Google TypeScript Style Guide](https://google.github.io/styleguide/tsguide.html)
  for additional best practices.
- All TypeScript files must begin with the required file header (see general
  instructions for details and TypeScript example).

## Core Principles

- Readability: Code should be clear and understandable
- Consistency: Follow established patterns and conventions
- Type Safety: Use TypeScript features to catch errors at compile time
- Simplicity: Prefer simple, straightforward solutions

## File Organization

- Use `.ts` for TypeScript files, `.tsx` for TypeScript with JSX
- Use ES6 import/export style
- License header (if required), then imports, then main export, then
  implementation

## Naming Conventions

- camelCase for variables and functions
- PascalCase for classes and interfaces
- SCREAMING_SNAKE_CASE for module-level constants
- Use descriptive names, avoid abbreviations
- Use PascalCase for enum names and members

## Type Annotations

- Always annotate function parameters and return types
- Use arrow functions for short functions
- Use explicit types for complex objects
- Use interfaces for object shapes that might be extended
- Use type aliases for unions, primitives, or computed types

## Google TypeScript Style Guide (Complete)

This section contains the complete Google TypeScript Style Guide content for
comprehensive reference.

### Introduction

This Style Guide uses RFC 2119 terminology when using the phrases must, must
not, should, should not, and may. The terms prefer and avoid correspond to
should and should not, respectively. Imperative and declarative statements are
prescriptive and correspond to must.

All examples given are non-normative and serve only to illustrate the normative
language of the style guide. That is, while the examples are in Google Style,
they may not illustrate the only stylish way to represent the code. Optional
formatting choices made in examples must not be enforced as rules.

### Source File Basics

#### File Encoding: UTF-8

Source files are encoded in UTF-8.

##### Whitespace Characters

Aside from the line terminator sequence, the ASCII horizontal space character
(0x20) is the only whitespace character that appears anywhere in a source file.
This implies that all other whitespace characters in string literals are
escaped.

##### Special Escape Sequences

For any character that has a special escape sequence (`\'`, `\"`, `\\`, `\b`,
`\f`, `\n`, `\r`, `\t`, `\v`), that sequence is used rather than the
corresponding numeric escape (e.g `\x0a`, `\u000a`, or `\u{a}`). Legacy octal
escapes are never used.

##### Non-ASCII Characters

For the remaining non-ASCII characters, use the actual Unicode character (e.g.
`∞`). For non-printable characters, the equivalent hex or Unicode escapes (e.g.
`\u221e`) can be used along with an explanatory comment.

```typescript
// Perfectly clear, even without a comment.
const units = 'μs';

// Use escapes for non-printable characters.
const output = '\ufeff' + content; // byte order mark
```

### Source File Structure

Files consist of the following, in order:

1. Copyright information, if present
2. JSDoc with `@fileoverview`, if present
3. Imports, if present
4. The file's implementation

Exactly one blank line separates each section that is present.

#### Copyright Information

If license or copyright information is necessary in a file, add it in a JSDoc at
the top of the file.

#### @fileoverview JSDoc

A file may have a top-level `@fileoverview` JSDoc. If present, it may provide a
description of the file's content, its uses, or information about its
dependencies. Wrapped lines are not indented.

```typescript
/**
 * @fileoverview Description of file. Lorem ipsum dolor sit amet, consectetur
 * adipiscing elit, sed do eiusmod tempor incididunt.
 */
```

#### Imports

There are four variants of import statements in ES6 and TypeScript:

| Type        | Import Style                     | Usage                                                   |
| ----------- | -------------------------------- | ------------------------------------------------------- |
| module      | `import * as foo from '...';`    | TypeScript imports                                      |
| named       | `import {SomeThing} from '...';` | TypeScript imports                                      |
| default     | `import SomeThing from '...';`   | Only for other external code that requires them         |
| side-effect | `import '...';`                  | Only to import libraries for their side-effects on load |

```typescript
// Good: choose between two options as appropriate (see below).
import * as ng from '@angular/core';
import { Foo } from './foo';

// Only when needed: default imports.
import Button from 'Button';

// Sometimes needed to import libraries for their side effects:
import 'jasmine';
import '@polymer/paper-button';
```

##### Import Paths

TypeScript code must use paths to import other TypeScript code. Paths may be
relative, i.e. starting with `.` or `..`, or rooted at the base directory, e.g.
`root/path/to/file`.

Code should use relative imports (`./foo`) rather than absolute imports
`path/to/foo` when referring to files within the same (logical) project as this
allows to move the project around without introducing changes in these imports.

##### Namespace versus Named Imports

Both namespace and named imports can be used.

Prefer named imports for symbols used frequently in a file or for symbols that
have clear names, for example Jasmine's `describe` and `it`. Named imports can
be aliased to clearer names as needed with `as`.

Prefer namespace imports when using many different symbols from large APIs. A
namespace import, despite using the `*` character, is not comparable to a
"wildcard" import as seen in other languages.

```typescript
// Bad: overlong import statement of needlessly namespaced names.
import {
  Item as TableviewItem,
  Header as TableviewHeader,
  Row as TableviewRow,
  Model as TableviewModel,
  Renderer as TableviewRenderer,
} from './tableview';

let item: TableviewItem | undefined;
```

```typescript
// Better: use the module for namespacing.
import * as tableview from './tableview';

let item: tableview.Item | undefined;
```

##### Renaming Imports

Code should fix name collisions by using a namespace import or renaming the
exports themselves. Code may rename imports
(`import {SomeThing as SomeOtherThing}`) if needed.

Three examples where renaming can be helpful:

1. If it's necessary to avoid collisions with other imported symbols.
2. If the imported symbol name is generated.
3. If importing symbols whose names are unclear by themselves, renaming can
   improve code clarity.

#### Exports

Use named exports in all code:

```typescript
// Use named exports:
export class Foo { ... }
```

Do not use default exports. This ensures that all imports follow a uniform
pattern.

```typescript
// Do not use default exports:
export default class Foo { ... } // BAD!
```

**Why?** Default exports provide no canonical name, which makes central
maintenance difficult with relatively little benefit to code owners, including
potentially decreased readability.

Named exports have the benefit of erroring when import statements try to import
something that hasn't been declared.

##### Export Visibility

TypeScript does not support restricting the visibility for exported symbols.
Only export symbols that are used outside of the module. Generally minimize the
exported API surface of modules.

##### Mutable Exports

Regardless of technical support, mutable exports can create hard to understand
and debug code, in particular with re-exports across multiple modules. One way
to paraphrase this style point is that `export let` is not allowed.

```typescript
export let foo = 3;
// In pure ES6, foo is mutable and importers will observe the value change after a second.
// In TS, if foo is re-exported by a second file, importers will not see the value change.
window.setTimeout(() => {
  foo = 4;
}, 1000 /* ms */);
```

If one needs to support externally accessible and mutable bindings, they should
instead use explicit getter functions.

##### Container Classes

Do not create container classes with static methods or properties for the sake
of namespacing.

```typescript
export class Container {
  static FOO = 1;
  static bar() {
    return 1;
  }
}
```

Instead, export individual constants and functions:

```typescript
export const FOO = 1;
export function bar() {
  return 1;
}
```

### Language Features

#### Local Variable Declarations

##### Use const and let

Always use `const` or `let` to declare variables. Use `const` by default, unless
a variable needs to be reassigned. Never use `var`.

```typescript
const foo = otherValue; // Use if "foo" never changes.
let bar = someValue; // Use if "bar" is ever assigned into later on.
```

`const` and `let` are block scoped, like variables in most other languages.
`var` in JavaScript is function scoped, which can cause difficult to understand
bugs. Don't use it.

Variables must not be used before their declaration.

##### One Variable Per Declaration

Every local variable declaration declares only one variable: declarations such
as `let a = 1, b = 2;` are not used.

#### Array Literals

##### Do not use the Array Constructor

Do not use the `Array()` constructor, with or without `new`. It has confusing
and contradictory usage:

```typescript
const a = new Array(2); // [undefined, undefined]
const b = new Array(2, 3); // [2, 3];
```

Instead, always use bracket notation to initialize arrays, or `from` to
initialize an `Array` with a certain size:

```typescript
const a = [2];
const b = [2, 3];

// Equivalent to Array(2):
const c = [];
c.length = 2;

// [0, 0, 0, 0, 0]
Array.from<number>({ length: 5 }).fill(0);
```

##### Do not define properties on arrays

Do not define or use non-numeric properties on an array (other than `length`).
Use a `Map` (or `Object`) instead.

##### Using spread syntax

Using spread syntax `[...foo];` is a convenient shorthand for shallow-copying or
concatenating iterables.

```typescript
const foo = [1];
const foo2 = [...foo, 6, 7];
const foo3 = [5, ...foo];

foo2[1] === 6;
foo3[1] === 1;
```

When using spread syntax, the value being spread must match what is being
created. When creating an array, only spread iterables. Primitives (including
`null` and `undefined`) must not be spread.

##### Array destructuring

Array literals may be used on the left-hand side of an assignment to perform
destructuring (such as when unpacking multiple values from a single array or
iterable). A final "rest" element may be included (with no space between the
`...` and the variable name). Elements should be omitted if they are unused.

```typescript
const [a, b, c, ...rest] = generateResults();
let [, b, , d] = someArray;
```

Destructuring may also be used for function parameters. Always specify `[]` as
the default value if a destructured array parameter is optional, and provide
default values on the left hand side:

```typescript
function destructured([a = 4, b = 2] = []) { … }
```

#### Object Literals

##### Do not use the Object Constructor

The `Object` constructor is disallowed. Use an object literal (`{}` or
`{a: 0, b: 1, c: 2}`) instead.

##### Iterating objects

Iterating objects with `for (... in ...)` is error prone. It will include
enumerable properties from the prototype chain.

Do not use unfiltered `for (... in ...)` statements:

```typescript
for (const x in someObj) {
  // x could come from some parent prototype!
}
```

Either filter values explicitly with an `if` statement, or use
`for (... of Object.keys(...))`.

```typescript
for (const x in someObj) {
  if (!someObj.hasOwnProperty(x)) continue;
  // now x was definitely defined on someObj
}
for (const x of Object.keys(someObj)) {
  // note: for _of_!
  // now x was definitely defined on someObj
}
for (const [key, value] of Object.entries(someObj)) {
  // note: for _of_!
  // now key was definitely defined on someObj
}
```

##### Using spread syntax

Using spread syntax `{...bar}` is a convenient shorthand for creating a shallow
copy of an object. When using spread syntax in object initialization, later
values replace earlier values at the same key.

```typescript
const foo = { num: 1 };
const foo2 = { ...foo, num: 5 };
const foo3 = { num: 5, ...foo };

foo2.num === 5;
foo3.num === 1;
```

When using spread syntax, the value being spread must match what is being
created. That is, when creating an object, only objects may be spread; arrays
and primitives (including `null` and `undefined`) must not be spread.

##### Computed property names

Computed property names (e.g. `{['key' + foo()]: 42}`) are allowed, and are
considered dict-style (quoted) keys (i.e., must not be mixed with non-quoted
keys) unless the computed property is a symbol (e.g. `[Symbol.iterator]`).

##### Object destructuring

Object destructuring patterns may be used on the left-hand side of an assignment
to perform destructuring and unpack multiple values from a single object.

Destructured objects may also be used as function parameters, but should be kept
as simple as possible: a single level of unquoted shorthand properties. Deeper
levels of nesting and computed properties may not be used in parameter
destructuring.

```typescript
interface Options {
  /** The number of times to do something. */
  num?: number;
  /** A string to do stuff to. */
  str?: string;
}

function destructured({ num, str = 'default' }: Options = {}) {}
```

#### Classes

##### Class Declarations

Class declarations must not be terminated with semicolons:

```typescript
class Foo {}
```

In contrast, statements that contain class expressions must be terminated with a
semicolon:

```typescript
export const Baz = class extends Bar {
  method(): number {
    return this.x;
  }
}; // Semicolon here as this is a statement, not a declaration
```

##### Class Method Declarations

Class method declarations must not use a semicolon to separate individual method
declarations:

```typescript
class Foo {
  doThing() {
    console.log('A');
  }
}
```

Method declarations should be separated from surrounding code by a single blank
line.

###### Overriding toString

The `toString` method may be overridden, but must always succeed and never have
visible side effects.

##### Static Methods

###### Avoid private static methods

Where it does not interfere with readability, prefer module-local functions over
private static methods.

###### Do not rely on dynamic dispatch

Code should not rely on dynamic dispatch of static methods. Static methods
should only be called on the base class itself (which defines it directly).

###### Avoid static this references

Code must not use `this` in a static context.

```typescript
class ShoeStore {
  static storage: Storage = ...;

  static isAvailable(s: Shoe) {
    // Bad: do not use `this` in a static method.
    return this.storage.has(s.id);
  }
}
```

##### Constructors

Constructor calls must use parentheses, even when no arguments are passed:

```typescript
const x = new Foo();
```

It is unnecessary to provide an empty constructor or one that simply delegates
into its parent class because ES2015 provides a default class constructor if one
is not specified.

##### Class Members

###### No #private fields

Do not use private fields (also known as private identifiers):

```typescript
class Clazz {
  #ident = 1;
}
```

Instead, use TypeScript's visibility annotations:

```typescript
class Clazz {
  private ident = 1;
}
```

**Why?** Private identifiers cause substantial emit size and performance
regressions when down-leveled by TypeScript, and are unsupported before ES2015.

###### Use readonly

Mark properties that are never reassigned outside of the constructor with the
`readonly` modifier.

###### Parameter properties

Rather than plumbing an obvious initializer through to a class member, use a
TypeScript parameter property.

```typescript
class Foo {
  constructor(private readonly barService: BarService) {}
}
```

###### Field initializers

If a class member is not a parameter, initialize it where it's declared, which
sometimes lets you drop the constructor entirely.

```typescript
class Foo {
  private readonly userList: string[] = [];
}
```

###### Getters and setters

Getters and setters, also known as accessors, for class members may be used. The
getter method must be a pure function (i.e., result is consistent and has no
side effects: getters must not change observable state).

```typescript
class Foo {
  constructor(private readonly someService: SomeService) {}

  get someMember(): string {
    return this.someService.someVariable;
  }

  set someMember(newValue: string) {
    this.someService.someVariable = newValue;
  }
}
```

##### Visibility

Restricting visibility of properties, methods, and entire types helps with
keeping code decoupled.

- Limit symbol visibility as much as possible.
- Consider converting private methods to non-exported functions within the same
  file but outside of any class.
- TypeScript symbols are public by default. Never use the `public` modifier
  except when declaring non-readonly public parameter properties (in
  constructors).

```typescript
class Foo {
  bar = new Bar(); // GOOD: public modifier not needed

  constructor(public baz: Baz) {} // public modifier allowed
}
```

#### Functions

##### Terminology

There are many different types of functions, with subtle distinctions between
them:

- "function declaration": a declaration using the `function` keyword
- "function expression": an expression using the `function` keyword
- "arrow function": an expression using the `=>` syntax
- "block body": right hand side of an arrow function with braces
- "concise body": right hand side of an arrow function without braces

##### Prefer function declarations for named functions

Prefer function declarations over arrow functions or function expressions when
defining named functions.

```typescript
function foo() {
  return 42;
}
```

Arrow functions may be used, for example, when an explicit type annotation is
required.

##### Nested functions

Functions nested within other methods or functions may use function declarations
or arrow functions, as appropriate. In method bodies in particular, arrow
functions are preferred because they have access to the outer `this`.

##### Do not use function expressions

Do not use function expressions. Use arrow functions instead.

```typescript
bar(() => {
  this.doSomething();
});
```

Exception: Function expressions may be used only if code has to dynamically
rebind `this` (but this is discouraged), or for generator functions (which do
not have an arrow syntax).

##### Arrow function bodies

Use arrow functions with concise bodies (i.e. expressions) or block bodies as
appropriate.

```typescript
// Block bodies are fine:
const receipts = books.map((b: Book) => {
  const receipt = payMoney(b.price);
  recordTransaction(receipt);
  return receipt;
});

// Concise bodies are fine, too, if the return value is used:
const longThings = myValues.filter(v => v.length > 1000).map(v => String(v));
```

Only use a concise body if the return value of the function is actually used.

##### Rebinding this

Function expressions and function declarations must not use `this` unless they
specifically exist to rebind the `this` pointer. Rebinding `this` can in most
cases be avoided by using arrow functions or explicit parameters.

```typescript
// Good: explicitly reference the object from an arrow function.
document.body.onclick = () => {
  document.body.textContent = 'hello';
};
```

##### Prefer passing arrow functions as callbacks

Callbacks can be invoked with unexpected arguments that can pass a type check
but still result in logical errors.

Avoid passing a named callback to a higher-order function, unless you are sure
of the stability of both functions' call signatures.

```typescript
// GOOD: Arguments are explicitly passed to the callback
const numbers = ['11', '5', '3'].map(n => parseInt(n));
// > [11, 5, 3]
```

### Naming

#### Identifiers

Identifiers must use only ASCII letters, digits, underscores (for constants and
structured test method names), and (rarely) the '$' sign.

##### Naming Style

TypeScript expresses information in types, so names should not be decorated with
information that is included in the type.

Some concrete examples of this rule:

- Do not use trailing or leading underscores for private properties or methods.
- Do not use the `opt_` prefix for optional parameters.
- Do not mark interfaces specially (`IMyInterface` or `MyFooInterface`) unless
  it's idiomatic in its environment.
- Suffixing `Observable`s with `$` is a common external convention and can help
  resolve confusion regarding observable values vs concrete values.

##### Descriptive Names

Names must be descriptive and clear to a new reader. Do not use abbreviations
that are ambiguous or unfamiliar to readers outside your project, and do not
abbreviate by deleting letters within a word.

Exception: Variables that are in scope for 10 lines or fewer, including
arguments that are not part of an exported API, may use short (e.g. single
letter) variable names.

```typescript
// Good identifiers:
errorCount; // No abbreviation.
dnsConnectionIndex; // Most people know what "DNS" stands for.
referrerUrl; // Ditto for "URL".
customerId; // "Id" is both ubiquitous and unlikely to be misunderstood.
```

```typescript
// Disallowed identifiers:
n; // Meaningless.
nErr; // Ambiguous abbreviation.
nCompConns; // Ambiguous abbreviation.
wgcConnections; // Only your group knows what this stands for.
pcReader; // Lots of things can be abbreviated "pc".
cstmrId; // Deletes internal letters.
kSecondsPerDay; // Do not use Hungarian notation.
customerID; // Incorrect camelcase of "ID".
```

##### Camel Case

Treat abbreviations like acronyms in names as whole words, i.e. use
`loadHttpUrl`, not `loadHTTPURL`, unless required by a platform name (e.g.
`XMLHttpRequest`).

##### Dollar Sign

Identifiers should not generally use `$`, except when required by naming
conventions for third party frameworks. See above for more on using `$` with
`Observable` values.

#### Rules by Identifier Type

Most identifier names should follow the casing in the table below, based on the
identifier's type.

| Style          | Type                                                                                                                   |
| -------------- | ---------------------------------------------------------------------------------------------------------------------- |
| UpperCamelCase | class / interface / type / enum / decorator / type parameters / component functions in TSX / JSXElement type parameter |
| lowerCamelCase | variable / parameter / function / method / property / module alias                                                     |
| CONSTANT_CASE  | global constant values, including enum values                                                                          |

##### Type Parameters

Type parameters, like in `Array<T>`, may use a single upper case character (`T`)
or `UpperCamelCase`.

##### Test Names

Test method names in xUnit-style test frameworks may be structured with `_`
separators, e.g. `testX_whenY_doesZ()`.

##### \_ prefix/suffix

Identifiers must not use `_` as a prefix or suffix.

This also means that `_` must not be used as an identifier by itself (e.g. to
indicate a parameter is unused).

##### Imports

Module namespace imports are `lowerCamelCase` while files are `snake_case`,
which means that imports correctly will not match in casing style, such as

```typescript
import * as fooBar from './foo_bar';
```

##### Constants

Immutable: `CONSTANT_CASE` indicates that a value is intended to not be changed,
and may be used for values that can technically be modified (i.e. values that
are not deeply frozen) to indicate to users that they must not be modified.

```typescript
const UNIT_SUFFIXES = {
  milliseconds: 'ms',
  seconds: 's',
};
// Even though per the rules of JavaScript UNIT_SUFFIXES is
// mutable, the uppercase shows users to not modify it.
```

A constant can also be a `static readonly` property of a class.

Global: Only symbols declared on the module level, static fields of module level
classes, and values of module level enums, may use `CONST_CASE`.

### Type System

#### Type Inference

Code may rely on type inference as implemented by the TypeScript compiler for
all type expressions (variables, fields, return types, etc).

```typescript
const x = 15; // Type inferred.
```

Leave out type annotations for trivially inferred types: variables or parameters
initialized to a `string`, `number`, `boolean`, `RegExp` literal or `new`
expression.

Explicitly specifying types may be required to prevent generic type parameters
from being inferred as `unknown`. For example, initializing generic types with
no values (e.g. empty arrays, objects, `Map`s, or `Set`s).

```typescript
const x = new Set<string>();
```

##### Return Types

Whether to include return type annotations for functions and methods is up to
the code author. Reviewers may ask for annotations to clarify complex return
types that are hard to understand.

There are two benefits to explicitly typing out the implicit return values of
functions and methods:

- More precise documentation to benefit readers of the code.
- Surface potential type errors faster in the future if there are code changes
  that change the return type of the function.

#### Undefined and null

TypeScript supports `undefined` and `null` types. Nullable types can be
constructed as a union type (`string|null`); similarly with `undefined`. There
is no special syntax for unions of `undefined` and `null`.

TypeScript code can use either `undefined` or `null` to denote absence of a
value, there is no general guidance to prefer one over the other. Many
JavaScript APIs use `undefined` (e.g. `Map.get`), while many DOM and Google APIs
use `null` (e.g. `Element.getAttribute`), so the appropriate absent value
depends on the context.

##### Nullable/undefined type aliases

Type aliases must not include `|null` or `|undefined` in a union type. Nullable
aliases typically indicate that null values are being passed around through too
many layers of an application, and this clouds the source of the original issue
that resulted in `null`.

Instead, code must only add `|null` or `|undefined` when the alias is actually
used. Code should deal with null values close to where they arise.

```typescript
// Better
type CoffeeResponse = Latte|Americano;

class CoffeeService {
  getLatte(): CoffeeResponse|undefined { ... };
}
```

##### Prefer optional over |undefined

In addition, TypeScript supports a special construct for optional parameters and
fields, using `?`:

```typescript
interface CoffeeOrder {
  sugarCubes: number;
  milk?: Whole|LowFat|HalfHalf;
}

function pourCoffee(volume?: Milliliter) { ... }
```

Optional parameters implicitly include `|undefined` in their type. However, they
are different in that they can be left out when constructing a value or calling
a method.

Use optional fields (on interfaces or classes) and parameters rather than a
`|undefined` type.

#### Use Structural Types

TypeScript's type system is structural, not nominal. That is, a value matches a
type if it has at least all the properties the type requires and the properties'
types match, recursively.

When providing a structural-based implementation, explicitly include the type at
the declaration of the symbol (this allows more precise type checking and error
reporting).

```typescript
const foo: Foo = {
  a: 123,
  b: 'abc',
};
```

Use interfaces to define structural types, not classes.

#### Prefer interfaces over type literal aliases

TypeScript supports type aliases for naming a type expression. This can be used
to name primitives, unions, tuples, and any other types.

However, when declaring types for objects, use interfaces instead of a type
alias for the object literal expression.

```typescript
interface User {
  firstName: string;
  lastName: string;
}
```

**Why?** These forms are nearly equivalent, so under the principle of just
choosing one out of two forms to prevent variation, we should choose one.
Additionally, there are interesting technical reasons to prefer interface.

#### Array<T> Type

For simple types (containing just alphanumeric characters and dot), use the
syntax sugar for arrays, `T[]` or `readonly T[]`, rather than the longer form
`Array<T>` or `ReadonlyArray<T>`.

For anything more complex, use the longer form `Array<T>`.

```typescript
let a: string[];
let b: readonly string[];
let c: ns.MyObj[];
let d: string[][];
let e: Array<{ n: number; s: string }>;
let f: Array<string | number>;
let g: ReadonlyArray<string | number>;
```

#### any Type

TypeScript's `any` type is a super and subtype of all other types, and allows
dereferencing all properties. As such, `any` is dangerous - it can mask severe
programming errors, and its use undermines the value of having static types in
the first place.

Consider not to use `any`. In circumstances where you want to use `any`,
consider one of:

- Provide a more specific type
- Use unknown
- Suppress the lint warning and document why

##### Using unknown over any

The `any` type allows assignment into any other type and dereferencing any
property off it. Often this behaviour is not necessary or desirable, and code
just needs to express that a type is unknown. Use the built-in type `unknown` in
that situation — it expresses the concept and is much safer as it does not allow
dereferencing arbitrary properties.

```typescript
// Can assign any value (including null or undefined) into this but cannot
// use it without narrowing the type or casting.
const val: unknown = value;
```

#### Control Structures

##### Equality checks

Always use triple equals (`===`) and not equals (`!==`). The double equality
operators cause error prone type coercions that are hard to understand and
slower to implement for JavaScript Virtual Machines.

```typescript
if (foo === 'bar' || baz !== bam) {
  // All good here.
}
```

Exception: Comparisons to the literal `null` value may use the `==` and `!=`
operators to cover both `null` and `undefined` values.

##### Type assertions

Type assertions (`x as SomeType`) and non-nullability assertions (`y!`) are
unsafe. Both only silence the TypeScript compiler, but do not insert any runtime
checks to match these assertions, so they can cause your program to crash at
runtime.

Instead of the following:

```typescript
(x as Foo).foo();
y!.bar();
```

When you want to assert a type or non-nullability the best answer is to
explicitly write a runtime check that performs that check.

```typescript
// assuming Foo is a class.
if (x instanceof Foo) {
  x.foo();
}

if (y) {
  y.bar();
}
```

###### Type assertion syntax

Type assertions must use the `as` syntax (as opposed to the angle brackets
syntax). This enforces parentheses around the assertion when accessing a member.

```typescript
// z must be Foo because ...
const x = (z as Foo).length;
```

### Comments and Documentation

#### JSDoc versus comments

There are two types of comments, JSDoc (`/** ... */`) and non-JSDoc ordinary
comments (`// ...` or `/* ... */`).

- Use `/** JSDoc */` comments for documentation, i.e. comments a user of the
  code should read.
- Use `// line comments` for implementation comments, i.e. comments that only
  concern the implementation of the code itself.

JSDoc comments are understood by tools (such as editors and documentation
generators), while ordinary comments are only for other humans.

#### Multi-line comments

Multi-line comments are indented at the same level as the surrounding code. They
must use multiple single-line comments (`//`-style), not block comment style
(`/* */`).

```typescript
// This is
// fine
```

Comments are not enclosed in boxes drawn with asterisks or other characters.

#### JSDoc general form

The basic formatting of JSDoc comments is as seen in this example:

```typescript
/**
 * Multiple lines of JSDoc text are written here,
 * wrapped normally.
 * @param arg A number to do something to.
 */
function doSomething(arg: number) { … }
```

or in this single-line example:

```typescript
/** This short jsdoc describes the function. */
function doSomething(arg: number) { … }
```

If a single-line comment overflows into multiple lines, it must use the
multi-line style with `/**` and `*/` on their own lines.

#### Markdown

JSDoc is written in Markdown, though it may include HTML when necessary.

#### Document all top-level exports of modules

Use `/** JSDoc */` comments to communicate information to the users of your
code. Avoid merely restating the property or parameter name. You should also
document all properties and methods (exported/public or not) whose purpose is
not immediately obvious from their name, as judged by your reviewer.

Exception: Symbols that are only exported to be consumed by tooling, such as
@NgModule classes, do not require comments.

#### Method and function comments

Method, parameter, and return descriptions may be omitted if they are obvious
from the rest of the method's JSDoc or from the method name and type signature.

Method descriptions begin with a verb phrase that describes what the method
does. This phrase is not an imperative sentence, but instead is written in the
third person, as if there is an implied "This method ..." before it.

#### JSDoc type annotations

JSDoc type annotations are redundant in TypeScript source code. Do not declare
types in `@param` or `@return` blocks, do not write `@implements`, `@enum`,
`@private`, `@override` etc. on code that uses the `implements`, `enum`,
`private`, `override` etc. keywords.

### Disallowed Features

#### Wrapper objects for primitive types

TypeScript code must not instantiate the wrapper classes for the primitive types
`String`, `Boolean`, and `Number`. Wrapper classes have surprising behavior,
such as `new Boolean(false)` evaluating to `true`.

```typescript
const s = new String('hello'); // BAD
const b = new Boolean(false); // BAD
const n = new Number(5); // BAD
```

The wrappers may be called as functions for coercing (which is preferred over
using `+` or concatenating the empty string) or creating symbols.

#### Automatic Semicolon Insertion

Do not rely on Automatic Semicolon Insertion (ASI). Explicitly end all
statements using a semicolon. This prevents bugs due to incorrect semicolon
insertions and ensures compatibility with tools with limited ASI support.

#### Const enums

Code must not use `const enum`; use plain `enum` instead.

**Why?** TypeScript enums already cannot be mutated; `const enum` is a separate
language feature related to optimization that makes the enum invisible to
JavaScript users of the module.

#### Debugger statements

Debugger statements must not be included in production code.

```typescript
function debugMe() {
  debugger; // BAD
}
```

#### Dynamic code evaluation

Do not use `eval` or the `Function(...string)` constructor (except for code
loaders). These features are potentially dangerous and simply do not work in
environments using strict Content Security Policies.

#### Non-standard features

Do not use non-standard ECMAScript or Web Platform features.

This includes:

- Old features that have been marked deprecated or removed entirely from
  ECMAScript / the Web Platform
- New ECMAScript features that are not yet standardized
- Proposed but not-yet-complete web standards
- Non-standard language "extensions" (such as those provided by some external
  transpilers)

#### Modifying builtin objects

Never modify builtin types, either by adding methods to their constructors or to
their prototypes. Avoid depending on libraries that do this.

Do not add symbols to the global object unless absolutely necessary (e.g.
required by a third-party API).

This comprehensive TypeScript Style Guide provides detailed guidance for writing
consistent, maintainable TypeScript code following Google's established best
practices.

- Use extends for generic constraints
- Use built-in utility types

## Code Formatting

- Maximum 80 characters per line
- Use 2 spaces for indentation, no tabs
- Always use semicolons
- Use single quotes for strings, template literals for interpolation
- Use trailing commas in multiline structures

## Best Practices

- Use strict null checks
- Use array methods instead of loops
- Use object spread for copying
- Use destructuring for extraction
- Prefer async/await over promises
- Keep functions small and focused
- Use pure functions when possible
- Use function overloads for different signatures

## Testing

- Use descriptive test names
- Follow AAA pattern (Arrange, Act, Assert)
- Use table-driven tests for multiple scenarios

## Required File Header

All TypeScript files must begin with a standard header as described in the
[general coding instructions](general-coding.instructions.md). Example for
TypeScript:

```typescript
// file: path/to/file.ts
// version: 1.0.0
// guid: 123e4567-e89b-12d3-a456-426614174000
```

<!-- file: .github/instructions/typescript.instructions.md -->
<!-- version: 1.2.1 -->
<!-- guid: ts123456-e89b-12d3-a456-426614174000 -->
<!-- last-edited: 2026-01-19 -->
<!-- DO NOT EDIT: This file is managed centrally in ghcommon repository -->
<!-- To update: Create an issue/PR in jdfalk/ghcommon -->

<!-- prettier-ignore-start -->
<!-- markdownlint-disable -->
---
applyTo: "**/*.ts"
description: |
  TypeScript language-specific coding, documentation, and testing rules for Copilot/AI agents and VS Code Copilot customization. These rules extend the general instructions in `general-coding.instructions.md` and merge all unique content from the Google TypeScript Style Guide.
---
<!-- markdownlint-enable -->
<!-- prettier-ignore-end -->

# TypeScript Coding Instructions

- Follow the [general coding instructions](general-coding.instructions.md).
- Follow the
  [Google TypeScript Style Guide](https://google.github.io/styleguide/tsguide.html)
  for additional best practices.
- All TypeScript files must begin with the required file header (see general
  instructions for details and TypeScript example).

## Core Principles

- Readability: Code should be clear and understandable
- Consistency: Follow established patterns and conventions
- Type Safety: Use TypeScript features to catch errors at compile time
- Simplicity: Prefer simple, straightforward solutions

## File Organization

- Use `.ts` for TypeScript files, `.tsx` for TypeScript with JSX
- Use ES6 import/export style
- License header (if required), then imports, then main export, then
  implementation

## Naming Conventions

- camelCase for variables and functions
- PascalCase for classes and interfaces
- SCREAMING_SNAKE_CASE for module-level constants
- Use descriptive names, avoid abbreviations
- Use PascalCase for enum names and members

## Type Annotations

- Always annotate function parameters and return types
- Use arrow functions for short functions
- Use explicit types for complex objects
- Use interfaces for object shapes that might be extended
- Use type aliases for unions, primitives, or computed types

## Google TypeScript Style Guide (Complete)

This section contains the complete Google TypeScript Style Guide content for
comprehensive reference.

### Introduction

This Style Guide uses RFC 2119 terminology when using the phrases must, must
not, should, should not, and may. The terms prefer and avoid correspond to
should and should not, respectively. Imperative and declarative statements are
prescriptive and correspond to must.

All examples given are non-normative and serve only to illustrate the normative
language of the style guide. That is, while the examples are in Google Style,
they may not illustrate the only stylish way to represent the code. Optional
formatting choices made in examples must not be enforced as rules.

### Source File Basics

#### File Encoding: UTF-8

Source files are encoded in UTF-8.

##### Whitespace Characters

Aside from the line terminator sequence, the ASCII horizontal space character
(0x20) is the only whitespace character that appears anywhere in a source file.
This implies that all other whitespace characters in string literals are
escaped.

##### Special Escape Sequences

For any character that has a special escape sequence (`\'`, `\"`, `\\`, `\b`,
`\f`, `\n`, `\r`, `\t`, `\v`), that sequence is used rather than the
corresponding numeric escape (e.g `\x0a`, `\u000a`, or `\u{a}`). Legacy octal
escapes are never used.

##### Non-ASCII Characters

For the remaining non-ASCII characters, use the actual Unicode character (e.g.
`∞`). For non-printable characters, the equivalent hex or Unicode escapes (e.g.
`\u221e`) can be used along with an explanatory comment.

```typescript
// Perfectly clear, even without a comment.
const units = 'μs';

// Use escapes for non-printable characters.
const output = '\ufeff' + content; // byte order mark
```

### Source File Structure

Files consist of the following, in order:

1. Copyright information, if present
2. JSDoc with `@fileoverview`, if present
3. Imports, if present
4. The file's implementation

Exactly one blank line separates each section that is present.

#### Copyright Information

If license or copyright information is necessary in a file, add it in a JSDoc at
the top of the file.

#### @fileoverview JSDoc

A file may have a top-level `@fileoverview` JSDoc. If present, it may provide a
description of the file's content, its uses, or information about its
dependencies. Wrapped lines are not indented.

```typescript
/**
 * @fileoverview Description of file. Lorem ipsum dolor sit amet, consectetur
 * adipiscing elit, sed do eiusmod tempor incididunt.
 */
```

#### Imports

There are four variants of import statements in ES6 and TypeScript:

| Type        | Import Style                     | Usage                                                   |
| ----------- | -------------------------------- | ------------------------------------------------------- |
| module      | `import * as foo from '...';`    | TypeScript imports                                      |
| named       | `import {SomeThing} from '...';` | TypeScript imports                                      |
| default     | `import SomeThing from '...';`   | Only for other external code that requires them         |
| side-effect | `import '...';`                  | Only to import libraries for their side-effects on load |

```typescript
// Good: choose between two options as appropriate (see below).
import * as ng from '@angular/core';
import { Foo } from './foo';

// Only when needed: default imports.
import Button from 'Button';

// Sometimes needed to import libraries for their side effects:
import 'jasmine';
import '@polymer/paper-button';
```

##### Import Paths

TypeScript code must use paths to import other TypeScript code. Paths may be
relative, i.e. starting with `.` or `..`, or rooted at the base directory, e.g.
`root/path/to/file`.

Code should use relative imports (`./foo`) rather than absolute imports
`path/to/foo` when referring to files within the same (logical) project as this
allows to move the project around without introducing changes in these imports.

##### Namespace versus Named Imports

Both namespace and named imports can be used.

Prefer named imports for symbols used frequently in a file or for symbols that
have clear names, for example Jasmine's `describe` and `it`. Named imports can
be aliased to clearer names as needed with `as`.

Prefer namespace imports when using many different symbols from large APIs. A
namespace import, despite using the `*` character, is not comparable to a
"wildcard" import as seen in other languages.

```typescript
// Bad: overlong import statement of needlessly namespaced names.
import {
  Item as TableviewItem,
  Header as TableviewHeader,
  Row as TableviewRow,
  Model as TableviewModel,
  Renderer as TableviewRenderer,
} from './tableview';

let item: TableviewItem | undefined;
```

```typescript
// Better: use the module for namespacing.
import * as tableview from './tableview';

let item: tableview.Item | undefined;
```

##### Renaming Imports

Code should fix name collisions by using a namespace import or renaming the
exports themselves. Code may rename imports
(`import {SomeThing as SomeOtherThing}`) if needed.

Three examples where renaming can be helpful:

1. If it's necessary to avoid collisions with other imported symbols.
2. If the imported symbol name is generated.
3. If importing symbols whose names are unclear by themselves, renaming can
   improve code clarity.

#### Exports

Use named exports in all code:

```typescript
// Use named exports:
export class Foo { ... }
```

Do not use default exports. This ensures that all imports follow a uniform
pattern.

```typescript
// Do not use default exports:
export default class Foo { ... } // BAD!
```

**Why?** Default exports provide no canonical name, which makes central
maintenance difficult with relatively little benefit to code owners, including
potentially decreased readability.

Named exports have the benefit of erroring when import statements try to import
something that hasn't been declared.

##### Export Visibility

TypeScript does not support restricting the visibility for exported symbols.
Only export symbols that are used outside of the module. Generally minimize the
exported API surface of modules.

##### Mutable Exports

Regardless of technical support, mutable exports can create hard to understand
and debug code, in particular with re-exports across multiple modules. One way
to paraphrase this style point is that `export let` is not allowed.

```typescript
export let foo = 3;
// In pure ES6, foo is mutable and importers will observe the value change after a second.
// In TS, if foo is re-exported by a second file, importers will not see the value change.
window.setTimeout(() => {
  foo = 4;
}, 1000 /* ms */);
```

If one needs to support externally accessible and mutable bindings, they should
instead use explicit getter functions.

##### Container Classes

Do not create container classes with static methods or properties for the sake
of namespacing.

```typescript
export class Container {
  static FOO = 1;
  static bar() {
    return 1;
  }
}
```

Instead, export individual constants and functions:

```typescript
export const FOO = 1;
export function bar() {
  return 1;
}
```

### Language Features

#### Local Variable Declarations

##### Use const and let

Always use `const` or `let` to declare variables. Use `const` by default, unless
a variable needs to be reassigned. Never use `var`.

```typescript
const foo = otherValue; // Use if "foo" never changes.
let bar = someValue; // Use if "bar" is ever assigned into later on.
```

`const` and `let` are block scoped, like variables in most other languages.
`var` in JavaScript is function scoped, which can cause difficult to understand
bugs. Don't use it.

Variables must not be used before their declaration.

##### One Variable Per Declaration

Every local variable declaration declares only one variable: declarations such
as `let a = 1, b = 2;` are not used.

#### Array Literals

##### Do not use the Array Constructor

Do not use the `Array()` constructor, with or without `new`. It has confusing
and contradictory usage:

```typescript
const a = new Array(2); // [undefined, undefined]
const b = new Array(2, 3); // [2, 3];
```

Instead, always use bracket notation to initialize arrays, or `from` to
initialize an `Array` with a certain size:

```typescript
const a = [2];
const b = [2, 3];

// Equivalent to Array(2):
const c = [];
c.length = 2;

// [0, 0, 0, 0, 0]
Array.from<number>({ length: 5 }).fill(0);
```

##### Do not define properties on arrays

Do not define or use non-numeric properties on an array (other than `length`).
Use a `Map` (or `Object`) instead.

##### Using spread syntax

Using spread syntax `[...foo];` is a convenient shorthand for shallow-copying or
concatenating iterables.

```typescript
const foo = [1];
const foo2 = [...foo, 6, 7];
const foo3 = [5, ...foo];

foo2[1] === 6;
foo3[1] === 1;
```

When using spread syntax, the value being spread must match what is being
created. When creating an array, only spread iterables. Primitives (including
`null` and `undefined`) must not be spread.

##### Array destructuring

Array literals may be used on the left-hand side of an assignment to perform
destructuring (such as when unpacking multiple values from a single array or
iterable). A final "rest" element may be included (with no space between the
`...` and the variable name). Elements should be omitted if they are unused.

```typescript
const [a, b, c, ...rest] = generateResults();
let [, b, , d] = someArray;
```

Destructuring may also be used for function parameters. Always specify `[]` as
the default value if a destructured array parameter is optional, and provide
default values on the left hand side:

```typescript
function destructured([a = 4, b = 2] = []) { … }
```

#### Object Literals

##### Do not use the Object Constructor

The `Object` constructor is disallowed. Use an object literal (`{}` or
`{a: 0, b: 1, c: 2}`) instead.

##### Iterating objects

Iterating objects with `for (... in ...)` is error prone. It will include
enumerable properties from the prototype chain.

Do not use unfiltered `for (... in ...)` statements:

```typescript
for (const x in someObj) {
  // x could come from some parent prototype!
}
```

Either filter values explicitly with an `if` statement, or use
`for (... of Object.keys(...))`.

```typescript
for (const x in someObj) {
  if (!someObj.hasOwnProperty(x)) continue;
  // now x was definitely defined on someObj
}
for (const x of Object.keys(someObj)) {
  // note: for _of_!
  // now x was definitely defined on someObj
}
for (const [key, value] of Object.entries(someObj)) {
  // note: for _of_!
  // now key was definitely defined on someObj
}
```

##### Using spread syntax

Using spread syntax `{...bar}` is a convenient shorthand for creating a shallow
copy of an object. When using spread syntax in object initialization, later
values replace earlier values at the same key.

```typescript
const foo = { num: 1 };
const foo2 = { ...foo, num: 5 };
const foo3 = { num: 5, ...foo };

foo2.num === 5;
foo3.num === 1;
```

When using spread syntax, the value being spread must match what is being
created. That is, when creating an object, only objects may be spread; arrays
and primitives (including `null` and `undefined`) must not be spread.

##### Computed property names

Computed property names (e.g. `{['key' + foo()]: 42}`) are allowed, and are
considered dict-style (quoted) keys (i.e., must not be mixed with non-quoted
keys) unless the computed property is a symbol (e.g. `[Symbol.iterator]`).

##### Object destructuring

Object destructuring patterns may be used on the left-hand side of an assignment
to perform destructuring and unpack multiple values from a single object.

Destructured objects may also be used as function parameters, but should be kept
as simple as possible: a single level of unquoted shorthand properties. Deeper
levels of nesting and computed properties may not be used in parameter
destructuring.

```typescript
interface Options {
  /** The number of times to do something. */
  num?: number;
  /** A string to do stuff to. */
  str?: string;
}

function destructured({ num, str = 'default' }: Options = {}) {}
```

#### Classes

##### Class Declarations

Class declarations must not be terminated with semicolons:

```typescript
class Foo {}
```

In contrast, statements that contain class expressions must be terminated with a
semicolon:

```typescript
export const Baz = class extends Bar {
  method(): number {
    return this.x;
  }
}; // Semicolon here as this is a statement, not a declaration
```

##### Class Method Declarations

Class method declarations must not use a semicolon to separate individual method
declarations:

```typescript
class Foo {
  doThing() {
    console.log('A');
  }
}
```

Method declarations should be separated from surrounding code by a single blank
line.

###### Overriding toString

The `toString` method may be overridden, but must always succeed and never have
visible side effects.

##### Static Methods

###### Avoid private static methods

Where it does not interfere with readability, prefer module-local functions over
private static methods.

###### Do not rely on dynamic dispatch

Code should not rely on dynamic dispatch of static methods. Static methods
should only be called on the base class itself (which defines it directly).

###### Avoid static this references

Code must not use `this` in a static context.

```typescript
class ShoeStore {
  static storage: Storage = ...;

  static isAvailable(s: Shoe) {
    // Bad: do not use `this` in a static method.
    return this.storage.has(s.id);
  }
}
```

##### Constructors

Constructor calls must use parentheses, even when no arguments are passed:

```typescript
const x = new Foo();
```

It is unnecessary to provide an empty constructor or one that simply delegates
into its parent class because ES2015 provides a default class constructor if one
is not specified.

##### Class Members

###### No #private fields

Do not use private fields (also known as private identifiers):

```typescript
class Clazz {
  #ident = 1;
}
```

Instead, use TypeScript's visibility annotations:

```typescript
class Clazz {
  private ident = 1;
}
```

**Why?** Private identifiers cause substantial emit size and performance
regressions when down-leveled by TypeScript, and are unsupported before ES2015.

###### Use readonly

Mark properties that are never reassigned outside of the constructor with the
`readonly` modifier.

###### Parameter properties

Rather than plumbing an obvious initializer through to a class member, use a
TypeScript parameter property.

```typescript
class Foo {
  constructor(private readonly barService: BarService) {}
}
```

###### Field initializers

If a class member is not a parameter, initialize it where it's declared, which
sometimes lets you drop the constructor entirely.

```typescript
class Foo {
  private readonly userList: string[] = [];
}
```

###### Getters and setters

Getters and setters, also known as accessors, for class members may be used. The
getter method must be a pure function (i.e., result is consistent and has no
side effects: getters must not change observable state).

```typescript
class Foo {
  constructor(private readonly someService: SomeService) {}

  get someMember(): string {
    return this.someService.someVariable;
  }

  set someMember(newValue: string) {
    this.someService.someVariable = newValue;
  }
}
```

##### Visibility

Restricting visibility of properties, methods, and entire types helps with
keeping code decoupled.

- Limit symbol visibility as much as possible.
- Consider converting private methods to non-exported functions within the same
  file but outside of any class.
- TypeScript symbols are public by default. Never use the `public` modifier
  except when declaring non-readonly public parameter properties (in
  constructors).

```typescript
class Foo {
  bar = new Bar(); // GOOD: public modifier not needed

  constructor(public baz: Baz) {} // public modifier allowed
}
```

#### Functions

##### Terminology

There are many different types of functions, with subtle distinctions between
them:

- "function declaration": a declaration using the `function` keyword
- "function expression": an expression using the `function` keyword
- "arrow function": an expression using the `=>` syntax
- "block body": right hand side of an arrow function with braces
- "concise body": right hand side of an arrow function without braces

##### Prefer function declarations for named functions

Prefer function declarations over arrow functions or function expressions when
defining named functions.

```typescript
function foo() {
  return 42;
}
```

Arrow functions may be used, for example, when an explicit type annotation is
required.

##### Nested functions

Functions nested within other methods or functions may use function declarations
or arrow functions, as appropriate. In method bodies in particular, arrow
functions are preferred because they have access to the outer `this`.

##### Do not use function expressions

Do not use function expressions. Use arrow functions instead.

```typescript
bar(() => {
  this.doSomething();
});
```

Exception: Function expressions may be used only if code has to dynamically
rebind `this` (but this is discouraged), or for generator functions (which do
not have an arrow syntax).

##### Arrow function bodies

Use arrow functions with concise bodies (i.e. expressions) or block bodies as
appropriate.

```typescript
// Block bodies are fine:
const receipts = books.map((b: Book) => {
  const receipt = payMoney(b.price);
  recordTransaction(receipt);
  return receipt;
});

// Concise bodies are fine, too, if the return value is used:
const longThings = myValues.filter(v => v.length > 1000).map(v => String(v));
```

Only use a concise body if the return value of the function is actually used.

##### Rebinding this

Function expressions and function declarations must not use `this` unless they
specifically exist to rebind the `this` pointer. Rebinding `this` can in most
cases be avoided by using arrow functions or explicit parameters.

```typescript
// Good: explicitly reference the object from an arrow function.
document.body.onclick = () => {
  document.body.textContent = 'hello';
};
```

##### Prefer passing arrow functions as callbacks

Callbacks can be invoked with unexpected arguments that can pass a type check
but still result in logical errors.

Avoid passing a named callback to a higher-order function, unless you are sure
of the stability of both functions' call signatures.

```typescript
// GOOD: Arguments are explicitly passed to the callback
const numbers = ['11', '5', '3'].map(n => parseInt(n));
// > [11, 5, 3]
```

### Naming

#### Identifiers

Identifiers must use only ASCII letters, digits, underscores (for constants and
structured test method names), and (rarely) the '$' sign.

##### Naming Style

TypeScript expresses information in types, so names should not be decorated with
information that is included in the type.

Some concrete examples of this rule:

- Do not use trailing or leading underscores for private properties or methods.
- Do not use the `opt_` prefix for optional parameters.
- Do not mark interfaces specially (`IMyInterface` or `MyFooInterface`) unless
  it's idiomatic in its environment.
- Suffixing `Observable`s with `$` is a common external convention and can help
  resolve confusion regarding observable values vs concrete values.

##### Descriptive Names

Names must be descriptive and clear to a new reader. Do not use abbreviations
that are ambiguous or unfamiliar to readers outside your project, and do not
abbreviate by deleting letters within a word.

Exception: Variables that are in scope for 10 lines or fewer, including
arguments that are not part of an exported API, may use short (e.g. single
letter) variable names.

```typescript
// Good identifiers:
errorCount; // No abbreviation.
dnsConnectionIndex; // Most people know what "DNS" stands for.
referrerUrl; // Ditto for "URL".
customerId; // "Id" is both ubiquitous and unlikely to be misunderstood.
```

```typescript
// Disallowed identifiers:
n; // Meaningless.
nErr; // Ambiguous abbreviation.
nCompConns; // Ambiguous abbreviation.
wgcConnections; // Only your group knows what this stands for.
pcReader; // Lots of things can be abbreviated "pc".
cstmrId; // Deletes internal letters.
kSecondsPerDay; // Do not use Hungarian notation.
customerID; // Incorrect camelcase of "ID".
```

##### Camel Case

Treat abbreviations like acronyms in names as whole words, i.e. use
`loadHttpUrl`, not `loadHTTPURL`, unless required by a platform name (e.g.
`XMLHttpRequest`).

##### Dollar Sign

Identifiers should not generally use `$`, except when required by naming
conventions for third party frameworks. See above for more on using `$` with
`Observable` values.

#### Rules by Identifier Type

Most identifier names should follow the casing in the table below, based on the
identifier's type.

| Style          | Type                                                                                                                   |
| -------------- | ---------------------------------------------------------------------------------------------------------------------- |
| UpperCamelCase | class / interface / type / enum / decorator / type parameters / component functions in TSX / JSXElement type parameter |
| lowerCamelCase | variable / parameter / function / method / property / module alias                                                     |
| CONSTANT_CASE  | global constant values, including enum values                                                                          |

##### Type Parameters

Type parameters, like in `Array<T>`, may use a single upper case character (`T`)
or `UpperCamelCase`.

##### Test Names

Test method names in xUnit-style test frameworks may be structured with `_`
separators, e.g. `testX_whenY_doesZ()`.

##### \_ prefix/suffix

Identifiers must not use `_` as a prefix or suffix.

This also means that `_` must not be used as an identifier by itself (e.g. to
indicate a parameter is unused).

##### Imports

Module namespace imports are `lowerCamelCase` while files are `snake_case`,
which means that imports correctly will not match in casing style, such as

```typescript
import * as fooBar from './foo_bar';
```

##### Constants

Immutable: `CONSTANT_CASE` indicates that a value is intended to not be changed,
and may be used for values that can technically be modified (i.e. values that
are not deeply frozen) to indicate to users that they must not be modified.

```typescript
const UNIT_SUFFIXES = {
  milliseconds: 'ms',
  seconds: 's',
};
// Even though per the rules of JavaScript UNIT_SUFFIXES is
// mutable, the uppercase shows users to not modify it.
```

A constant can also be a `static readonly` property of a class.

Global: Only symbols declared on the module level, static fields of module level
classes, and values of module level enums, may use `CONST_CASE`.

### Type System

#### Type Inference

Code may rely on type inference as implemented by the TypeScript compiler for
all type expressions (variables, fields, return types, etc).

```typescript
const x = 15; // Type inferred.
```

Leave out type annotations for trivially inferred types: variables or parameters
initialized to a `string`, `number`, `boolean`, `RegExp` literal or `new`
expression.

Explicitly specifying types may be required to prevent generic type parameters
from being inferred as `unknown`. For example, initializing generic types with
no values (e.g. empty arrays, objects, `Map`s, or `Set`s).

```typescript
const x = new Set<string>();
```

##### Return Types

Whether to include return type annotations for functions and methods is up to
the code author. Reviewers may ask for annotations to clarify complex return
types that are hard to understand.

There are two benefits to explicitly typing out the implicit return values of
functions and methods:

- More precise documentation to benefit readers of the code.
- Surface potential type errors faster in the future if there are code changes
  that change the return type of the function.

#### Undefined and null

TypeScript supports `undefined` and `null` types. Nullable types can be
constructed as a union type (`string|null`); similarly with `undefined`. There
is no special syntax for unions of `undefined` and `null`.

TypeScript code can use either `undefined` or `null` to denote absence of a
value, there is no general guidance to prefer one over the other. Many
JavaScript APIs use `undefined` (e.g. `Map.get`), while many DOM and Google APIs
use `null` (e.g. `Element.getAttribute`), so the appropriate absent value
depends on the context.

##### Nullable/undefined type aliases

Type aliases must not include `|null` or `|undefined` in a union type. Nullable
aliases typically indicate that null values are being passed around through too
many layers of an application, and this clouds the source of the original issue
that resulted in `null`.

Instead, code must only add `|null` or `|undefined` when the alias is actually
used. Code should deal with null values close to where they arise.

```typescript
// Better
type CoffeeResponse = Latte|Americano;

class CoffeeService {
  getLatte(): CoffeeResponse|undefined { ... };
}
```

##### Prefer optional over |undefined

In addition, TypeScript supports a special construct for optional parameters and
fields, using `?`:

```typescript
interface CoffeeOrder {
  sugarCubes: number;
  milk?: Whole|LowFat|HalfHalf;
}

function pourCoffee(volume?: Milliliter) { ... }
```

Optional parameters implicitly include `|undefined` in their type. However, they
are different in that they can be left out when constructing a value or calling
a method.

Use optional fields (on interfaces or classes) and parameters rather than a
`|undefined` type.

#### Use Structural Types

TypeScript's type system is structural, not nominal. That is, a value matches a
type if it has at least all the properties the type requires and the properties'
types match, recursively.

When providing a structural-based implementation, explicitly include the type at
the declaration of the symbol (this allows more precise type checking and error
reporting).

```typescript
const foo: Foo = {
  a: 123,
  b: 'abc',
};
```

Use interfaces to define structural types, not classes.

#### Prefer interfaces over type literal aliases

TypeScript supports type aliases for naming a type expression. This can be used
to name primitives, unions, tuples, and any other types.

However, when declaring types for objects, use interfaces instead of a type
alias for the object literal expression.

```typescript
interface User {
  firstName: string;
  lastName: string;
}
```

**Why?** These forms are nearly equivalent, so under the principle of just
choosing one out of two forms to prevent variation, we should choose one.
Additionally, there are interesting technical reasons to prefer interface.

#### Array<T> Type

For simple types (containing just alphanumeric characters and dot), use the
syntax sugar for arrays, `T[]` or `readonly T[]`, rather than the longer form
`Array<T>` or `ReadonlyArray<T>`.

For anything more complex, use the longer form `Array<T>`.

```typescript
let a: string[];
let b: readonly string[];
let c: ns.MyObj[];
let d: string[][];
let e: Array<{ n: number; s: string }>;
let f: Array<string | number>;
let g: ReadonlyArray<string | number>;
```

#### any Type

TypeScript's `any` type is a super and subtype of all other types, and allows
dereferencing all properties. As such, `any` is dangerous - it can mask severe
programming errors, and its use undermines the value of having static types in
the first place.

Consider not to use `any`. In circumstances where you want to use `any`,
consider one of:

- Provide a more specific type
- Use unknown
- Suppress the lint warning and document why

##### Using unknown over any

The `any` type allows assignment into any other type and dereferencing any
property off it. Often this behaviour is not necessary or desirable, and code
just needs to express that a type is unknown. Use the built-in type `unknown` in
that situation — it expresses the concept and is much safer as it does not allow
dereferencing arbitrary properties.

```typescript
// Can assign any value (including null or undefined) into this but cannot
// use it without narrowing the type or casting.
const val: unknown = value;
```

#### Control Structures

##### Equality checks

Always use triple equals (`===`) and not equals (`!==`). The double equality
operators cause error prone type coercions that are hard to understand and
slower to implement for JavaScript Virtual Machines.

```typescript
if (foo === 'bar' || baz !== bam) {
  // All good here.
}
```

Exception: Comparisons to the literal `null` value may use the `==` and `!=`
operators to cover both `null` and `undefined` values.

##### Type assertions

Type assertions (`x as SomeType`) and non-nullability assertions (`y!`) are
unsafe. Both only silence the TypeScript compiler, but do not insert any runtime
checks to match these assertions, so they can cause your program to crash at
runtime.

Instead of the following:

```typescript
(x as Foo).foo();
y!.bar();
```

When you want to assert a type or non-nullability the best answer is to
explicitly write a runtime check that performs that check.

```typescript
// assuming Foo is a class.
if (x instanceof Foo) {
  x.foo();
}

if (y) {
  y.bar();
}
```

###### Type assertion syntax

Type assertions must use the `as` syntax (as opposed to the angle brackets
syntax). This enforces parentheses around the assertion when accessing a member.

```typescript
// z must be Foo because ...
const x = (z as Foo).length;
```

### Comments and Documentation

#### JSDoc versus comments

There are two types of comments, JSDoc (`/** ... */`) and non-JSDoc ordinary
comments (`// ...` or `/* ... */`).

- Use `/** JSDoc */` comments for documentation, i.e. comments a user of the
  code should read.
- Use `// line comments` for implementation comments, i.e. comments that only
  concern the implementation of the code itself.

JSDoc comments are understood by tools (such as editors and documentation
generators), while ordinary comments are only for other humans.

#### Multi-line comments

Multi-line comments are indented at the same level as the surrounding code. They
must use multiple single-line comments (`//`-style), not block comment style
(`/* */`).

```typescript
// This is
// fine
```

Comments are not enclosed in boxes drawn with asterisks or other characters.

#### JSDoc general form

The basic formatting of JSDoc comments is as seen in this example:

```typescript
/**
 * Multiple lines of JSDoc text are written here,
 * wrapped normally.
 * @param arg A number to do something to.
 */
function doSomething(arg: number) { … }
```

or in this single-line example:

```typescript
/** This short jsdoc describes the function. */
function doSomething(arg: number) { … }
```

If a single-line comment overflows into multiple lines, it must use the
multi-line style with `/**` and `*/` on their own lines.

#### Markdown

JSDoc is written in Markdown, though it may include HTML when necessary.

#### Document all top-level exports of modules

Use `/** JSDoc */` comments to communicate information to the users of your
code. Avoid merely restating the property or parameter name. You should also
document all properties and methods (exported/public or not) whose purpose is
not immediately obvious from their name, as judged by your reviewer.

Exception: Symbols that are only exported to be consumed by tooling, such as
@NgModule classes, do not require comments.

#### Method and function comments

Method, parameter, and return descriptions may be omitted if they are obvious
from the rest of the method's JSDoc or from the method name and type signature.

Method descriptions begin with a verb phrase that describes what the method
does. This phrase is not an imperative sentence, but instead is written in the
third person, as if there is an implied "This method ..." before it.

#### JSDoc type annotations

JSDoc type annotations are redundant in TypeScript source code. Do not declare
types in `@param` or `@return` blocks, do not write `@implements`, `@enum`,
`@private`, `@override` etc. on code that uses the `implements`, `enum`,
`private`, `override` etc. keywords.

### Disallowed Features

#### Wrapper objects for primitive types

TypeScript code must not instantiate the wrapper classes for the primitive types
`String`, `Boolean`, and `Number`. Wrapper classes have surprising behavior,
such as `new Boolean(false)` evaluating to `true`.

```typescript
const s = new String('hello'); // BAD
const b = new Boolean(false); // BAD
const n = new Number(5); // BAD
```

The wrappers may be called as functions for coercing (which is preferred over
using `+` or concatenating the empty string) or creating symbols.

#### Automatic Semicolon Insertion

Do not rely on Automatic Semicolon Insertion (ASI). Explicitly end all
statements using a semicolon. This prevents bugs due to incorrect semicolon
insertions and ensures compatibility with tools with limited ASI support.

#### Const enums

Code must not use `const enum`; use plain `enum` instead.

**Why?** TypeScript enums already cannot be mutated; `const enum` is a separate
language feature related to optimization that makes the enum invisible to
JavaScript users of the module.

#### Debugger statements

Debugger statements must not be included in production code.

```typescript
function debugMe() {
  debugger; // BAD
}
```

#### Dynamic code evaluation

Do not use `eval` or the `Function(...string)` constructor (except for code
loaders). These features are potentially dangerous and simply do not work in
environments using strict Content Security Policies.

#### Non-standard features

Do not use non-standard ECMAScript or Web Platform features.

This includes:

- Old features that have been marked deprecated or removed entirely from
  ECMAScript / the Web Platform
- New ECMAScript features that are not yet standardized
- Proposed but not-yet-complete web standards
- Non-standard language "extensions" (such as those provided by some external
  transpilers)

#### Modifying builtin objects

Never modify builtin types, either by adding methods to their constructors or to
their prototypes. Avoid depending on libraries that do this.

Do not add symbols to the global object unless absolutely necessary (e.g.
required by a third-party API).

This comprehensive TypeScript Style Guide provides detailed guidance for writing
consistent, maintainable TypeScript code following Google's established best
practices.

- Use extends for generic constraints
- Use built-in utility types

## Code Formatting

- Maximum 80 characters per line
- Use 2 spaces for indentation, no tabs
- Always use semicolons
- Use single quotes for strings, template literals for interpolation
- Use trailing commas in multiline structures

## Best Practices

- Use strict null checks
- Use array methods instead of loops
- Use object spread for copying
- Use destructuring for extraction
- Prefer async/await over promises
- Keep functions small and focused
- Use pure functions when possible
- Use function overloads for different signatures

## Testing

- Use descriptive test names
- Follow AAA pattern (Arrange, Act, Assert)
- Use table-driven tests for multiple scenarios

## Required File Header

All TypeScript files must begin with a standard header as described in the
[general coding instructions](general-coding.instructions.md). Example for
TypeScript:

```typescript
// file: path/to/file.ts
// version: 1.0.0
// guid: 123e4567-e89b-12d3-a456-426614174000
```
