package eval

// Builtin functions.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/user"
	"reflect"
	"runtime"
	"strconv"
)

var builtinFns []*BuiltinFn

func init() {
	// Needed to work around init loop.
	builtinFns = []*BuiltinFn{
		&BuiltinFn{":", nop},
		&BuiltinFn{"true", nop},

		&BuiltinFn{"print", wrapFn(print)},
		&BuiltinFn{"println", wrapFn(println)},

		&BuiltinFn{"into-lines", wrapFn(intoLines)},
		&BuiltinFn{"from-lines", wrapFn(fromLines)},

		&BuiltinFn{"rat", wrapFn(ratFn)},

		&BuiltinFn{"put", put},
		&BuiltinFn{"put-all", wrapFn(putAll)},
		&BuiltinFn{"unpack", wrapFn(unpack)},

		&BuiltinFn{"from-json", wrapFn(fromJSON)},

		&BuiltinFn{"typeof", typeof},

		&BuiltinFn{"failure", wrapFn(failure)},
		&BuiltinFn{"return", wrapFn(returnFn)},
		&BuiltinFn{"break", wrapFn(breakFn)},
		&BuiltinFn{"continue", wrapFn(continueFn)},

		&BuiltinFn{"each", wrapFn(each)},

		&BuiltinFn{"cd", cd},
		&BuiltinFn{"visited-dirs", wrapFn(visistedDirs)},
		&BuiltinFn{"jump-dir", wrapFn(jumpDir)},

		&BuiltinFn{"source", wrapFn(source)},

		&BuiltinFn{"+", wrapFn(plus)},
		&BuiltinFn{"-", wrapFn(minus)},
		&BuiltinFn{"*", wrapFn(times)},
		&BuiltinFn{"/", wrapFn(divide)},

		&BuiltinFn{"=", eq},

		&BuiltinFn{"bind", wrapFn(bind)},
		&BuiltinFn{"le", wrapFn(le)},

		&BuiltinFn{"-stack", wrapFn(_stack)},
	}
}

var (
	argsError  = NewFailure("args error")
	inputError = NewFailure("input error")
)

var (
	evalCtxType = reflect.TypeOf((*evalCtx)(nil))
	exitusType_ = reflect.TypeOf(Error{})
	valueType   = reflect.TypeOf((*Value)(nil)).Elem()
)

// wrapFn wraps an inner function into one suitable as a builtin function. It
// generates argument checking and conversion code according to the signature
// of the inner function. The inner function must accept evalCtx* as the first
// argument and return an exitus.
func wrapFn(inner interface{}) func(*evalCtx, []Value) Error {
	type_ := reflect.TypeOf(inner)
	if type_.In(0) != evalCtxType || type_.Out(0) != exitusType_ {
		panic("bad func")
	}

	requiredArgs := type_.NumIn() - 1
	isVariadic := type_.IsVariadic()
	var variadicType reflect.Type
	if isVariadic {
		requiredArgs -= 1
		variadicType = type_.In(type_.NumIn() - 1).Elem()
		if !supportedIn(variadicType) {
			panic("bad func argument")
		}
	}

	for i := 0; i < requiredArgs; i++ {
		if !supportedIn(type_.In(i + 1)) {
			panic("bad func argument")
		}
	}

	return func(ec *evalCtx, args []Value) Error {
		if len(args) < requiredArgs || (!isVariadic && len(args) > requiredArgs) {
			return argsError
		}
		callArgs := make([]reflect.Value, len(args)+1)
		callArgs[0] = reflect.ValueOf(ec)

		ok := convertArgs(args[:requiredArgs], callArgs[1:],
			func(i int) reflect.Type { return type_.In(i + 1) })
		if !ok {
			return argsError
		}
		if isVariadic {
			ok := convertArgs(args[requiredArgs:], callArgs[1+requiredArgs:],
				func(i int) reflect.Type { return variadicType })
			if !ok {
				return argsError
			}
		}
		return reflect.ValueOf(inner).Call(callArgs)[0].Interface().(Error)
	}
}

func supportedIn(t reflect.Type) bool {
	return t.Kind() == reflect.String || t.Kind() == reflect.Float64 ||
		t.Implements(valueType)
}

func convertArgs(args []Value, callArgs []reflect.Value, callType func(int) reflect.Type) bool {
	for i, arg := range args {
		var callArg interface{}
		switch callType(i).Kind() {
		case reflect.String:
			callArg = ToString(arg)
		case reflect.Float64:
			var err error
			callArg, err = toFloat(arg)
			if err != nil {
				return false
				// return err
			}
		default:
			if reflect.TypeOf(arg).ConvertibleTo(callType(i)) {
				callArg = arg
			} else {
				return false
				// return argsError
			}
		}
		callArgs[i] = reflect.ValueOf(callArg)
	}
	return true
}

func nop(ec *evalCtx, args []Value) Error {
	return OK
}

func put(ec *evalCtx, args []Value) Error {
	out := ec.ports[1].ch
	for _, a := range args {
		out <- a
	}
	return OK
}

func putAll(ec *evalCtx, lists ...*List) Error {
	out := ec.ports[1].ch
	for _, list := range lists {
		for _, x := range *list {
			out <- x
		}
	}
	return OK
}

func typeof(ec *evalCtx, args []Value) Error {
	out := ec.ports[1].ch
	for _, a := range args {
		out <- String(a.Type().String())
	}
	return OK
}

func failure(ec *evalCtx, arg Value) Error {
	out := ec.ports[1].ch
	out <- NewFailure(ToString(arg))
	return OK
}

func returnFn(ec *evalCtx) Error {
	return newFlow(Return)
}

func breakFn(ec *evalCtx) Error {
	return newFlow(Break)
}

func continueFn(ec *evalCtx) Error {
	return newFlow(Continue)
}

func print(ec *evalCtx, args ...string) Error {
	out := ec.ports[1].f
	for i, arg := range args {
		if i > 0 {
			out.WriteString(" ")
		}
		out.WriteString(arg)
	}
	return OK
}

func println(ec *evalCtx, args ...string) Error {
	print(ec, args...)
	ec.ports[1].f.WriteString("\n")
	return OK
}

func intoLines(ec *evalCtx) Error {
	in := ec.ports[0].ch
	out := ec.ports[1].f

	for v := range in {
		fmt.Fprintln(out, ToString(v))
	}
	return OK
}

func fromLines(ec *evalCtx) Error {
	in := ec.ports[0].f
	out := ec.ports[1].ch

	bufferedIn := bufio.NewReader(in)
	for {
		line, err := bufferedIn.ReadString('\n')
		if err == io.EOF {
			return OK
		} else if err != nil {
			return NewFailure(err.Error())
		}
		out <- String(line[:len(line)-1])
	}
}

func ratFn(ec *evalCtx, arg Value) Error {
	out := ec.ports[1].ch
	r, err := ToRat(arg)
	if err != nil {
		return NewFailure(err.Error())
	}
	out <- r
	return OK
}

// unpack takes any number of tables and output their list elements.
func unpack(ec *evalCtx) Error {
	in := ec.ports[0].ch
	out := ec.ports[1].ch

	for v := range in {
		if list, ok := v.(*List); !ok {
			return inputError
		} else {
			for _, e := range *list {
				out <- e
			}
		}
	}

	return OK
}

// fromJSON parses a stream of JSON data into Value's.
func fromJSON(ec *evalCtx) Error {
	in := ec.ports[0].f
	out := ec.ports[1].ch

	dec := json.NewDecoder(in)
	var v interface{}
	for {
		err := dec.Decode(&v)
		if err != nil {
			if err == io.EOF {
				return OK
			}
			return NewFailure(err.Error())
		}
		out <- FromJSONInterface(v)
	}
}

// each takes a single closure and applies it to all input values.
func each(ec *evalCtx, f *Closure) Error {
	in := ec.ports[0].ch
in:
	for v := range in {
		newec := ec.fork("closure of each")
		ex := f.Call(newec, []Value{v})
		newec.closePorts()

		switch ex.Sort {
		case Ok, Continue:
			// nop
		case Break:
			break in
		default:
			// TODO wrap it
			return ex
		}
	}
	return OK
}

func cd(ec *evalCtx, args []Value) Error {
	var dir string
	if len(args) == 0 {
		user, err := user.Current()
		if err == nil {
			dir = user.HomeDir
		} else {
			return NewFailure("cannot get current user: " + err.Error())
		}
	} else if len(args) == 1 {
		dir = ToString(args[0])
	} else {
		return argsError
	}

	return cdInner(dir, ec)
}

func cdInner(dir string, ec *evalCtx) Error {
	err := os.Chdir(dir)
	if err != nil {
		return NewFailure(err.Error())
	}
	if ec.store != nil {
		pwd, err := os.Getwd()
		// BUG(xiaq): Possible error of os.Getwd after cd-ing is ignored.
		if err == nil {
			ec.store.AddDir(pwd)
		}
	}
	return OK
}

var storeNotConnected = NewFailure("store not connected")

func visistedDirs(ec *evalCtx) Error {
	if ec.store == nil {
		return storeNotConnected
	}
	dirs, err := ec.store.ListDirs()
	if err != nil {
		return NewFailure("store error: " + err.Error())
	}
	out := ec.ports[1].ch
	for _, dir := range dirs {
		m := NewMap()
		m["path"] = String(dir.Path)
		m["score"] = String(fmt.Sprint(dir.Score))
		out <- m
	}
	return OK
}

var noMatchingDir = NewFailure("no matching directory")

func jumpDir(ec *evalCtx, arg string) Error {
	if ec.store == nil {
		return storeNotConnected
	}
	dirs, err := ec.store.FindDirs(arg)
	if err != nil {
		return NewFailure("store error: " + err.Error())
	}
	if len(dirs) == 0 {
		return noMatchingDir
	}
	dir := dirs[0].Path
	err = os.Chdir(dir)
	// TODO(xiaq): Remove directories that no longer exist
	if err != nil {
		return NewFailure(err.Error())
	}
	ec.store.AddDir(dir)
	return OK
}

func source(ec *evalCtx, fname string) Error {
	ec.Source(fname)
	return OK
}

func toFloat(arg Value) (float64, error) {
	arg, ok := arg.(String)
	if !ok {
		return 0, fmt.Errorf("must be string")
	}
	num, err := strconv.ParseFloat(string(arg.(String)), 64)
	if err != nil {
		return 0, err
	}
	return num, nil
}

func plus(ec *evalCtx, nums ...float64) Error {
	out := ec.ports[1].ch
	sum := 0.0
	for _, f := range nums {
		sum += f
	}
	out <- String(fmt.Sprintf("%g", sum))
	return OK
}

func minus(ec *evalCtx, sum float64, nums ...float64) Error {
	out := ec.ports[1].ch
	for _, f := range nums {
		sum -= f
	}
	out <- String(fmt.Sprintf("%g", sum))
	return OK
}

func times(ec *evalCtx, nums ...float64) Error {
	out := ec.ports[1].ch
	prod := 1.0
	for _, f := range nums {
		prod *= f
	}
	out <- String(fmt.Sprintf("%g", prod))
	return OK
}

func divide(ec *evalCtx, prod float64, nums ...float64) Error {
	out := ec.ports[1].ch
	for _, f := range nums {
		prod /= f
	}
	out <- String(fmt.Sprintf("%g", prod))
	return OK
}

func eq(ec *evalCtx, args []Value) Error {
	out := ec.ports[1].ch
	if len(args) == 0 {
		return argsError
	}
	for i := 0; i+1 < len(args); i++ {
		if !Eq(args[i], args[i+1]) {
			out <- Bool(false)
			return OK
		}
	}
	out <- Bool(true)
	return OK
}

var noEditor = NewFailure("no line editor")

func bind(ec *evalCtx, key string, function string) Error {
	if ec.Editor == nil {
		return noEditor
	}
	return ec.Editor.Bind(key, String(function))
}

func le(ec *evalCtx, name string, args ...Value) Error {
	if ec.Editor == nil {
		return noEditor
	}
	return ec.Editor.Call(name, args)
}

func _stack(ec *evalCtx) Error {
	out := ec.ports[1].f

	// XXX dup with main.go
	buf := make([]byte, 1024)
	for runtime.Stack(buf, true) == cap(buf) {
		buf = make([]byte, cap(buf)*2)
	}
	out.Write(buf)

	return OK
}
