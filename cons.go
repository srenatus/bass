package bass

import "context"

type Cons Pair

func NewConsList(vals ...Value) List {
	var list List = Empty{}
	for i := len(vals) - 1; i >= 0; i-- {
		list = Cons{
			A: vals[i],
			D: list,
		}
	}

	return list
}

func ToCons(list List) List {
	var empty Empty
	if err := list.Decode(&empty); err == nil {
		return list
	}

	var rest List
	if err := list.Rest().Decode(&rest); err == nil {
		return Cons{
			A: list.First(),
			D: ToCons(rest),
		}
	}

	return Cons{
		A: list.First(),
		D: list.Rest(),
	}
}

func (value Cons) String() string {
	return formatList(value, "[", "]")
}

func (value Cons) Equal(other Value) bool {
	var o Cons
	if err := other.Decode(&o); err != nil {
		return false
	}

	return value.A.Equal(o.A) && value.D.Equal(o.D)
}

func (value Cons) Decode(dest interface{}) error {
	switch x := dest.(type) {
	case *Cons:
		*x = value
		return nil
	case *List:
		*x = value
		return nil
	case *Value:
		*x = value
		return nil
	default:
		return DecodeError{
			Source:      value,
			Destination: dest,
		}
	}
}

// Eval evaluates both values in the pair.
func (value Cons) Eval(ctx context.Context, env *Env, cont Cont) ReadyCont {
	return value.A.Eval(ctx, env, Chain(cont, func(a Value) Value {
		return value.D.Eval(ctx, env, Chain(cont, func(d Value) Value {
			return cont.Call(Pair{
				A: a,
				D: d,
			}, nil)
		}))
	}))
}

func (value Cons) First() Value {
	return value.A
}

func (value Cons) Rest() Value {
	return value.D
}
