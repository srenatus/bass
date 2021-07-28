package bass

import (
	"context"
	"fmt"
)

type Pair struct {
	A Value
	D Value
}

var _ Value = Pair{}

func (value Pair) String() string {
	return formatList(value, "(", ")")
}

func (value Pair) Equal(other Value) bool {
	var o Pair
	if err := other.Decode(&o); err != nil {
		return false
	}

	return value.A.Equal(o.A) && value.D.Equal(o.D)
}

func (value Pair) Decode(dest interface{}) error {
	switch x := dest.(type) {
	case *Pair:
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

var _ List = Pair{}

func (value Pair) First() Value {
	return value.A
}

func (value Pair) Rest() Value {
	return value.D
}

// Pair combines the first operand with the second operand.
//
// If the first value is not a Combiner, an error is returned.
func (value Pair) Eval(ctx context.Context, env *Env, cont Cont) ReadyCont {
	return value.A.Eval(ctx, env, Chain(cont, func(f Value) Value {
		var combiner Combiner
		err := f.Decode(&combiner)
		if err != nil {
			return cont.Call(nil, fmt.Errorf("apply %s: %w", f, err))
		}

		return combiner.Call(ctx, value.D, env, cont)
	}))
}

func formatList(list List, odelim, cdelim string) string {
	out := odelim

	for list != (Empty{}) {
		out += list.First().String()

		var empty Empty
		err := list.Rest().Decode(&empty)
		if err == nil {
			break
		}

		err = list.Rest().Decode(&list)
		if err != nil {
			out += " . "
			out += list.Rest().String()
			break
		}

		out += " "
	}

	out += cdelim

	return out
}
