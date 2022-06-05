package bass

import (
	"fmt"

	"github.com/vito/bass/pkg/proto"
)

type ProtoMarshaler interface {
	MarshalProto() (proto.Message, error)
}

func MarshalProto(val Value) (*proto.Value, error) {
	dm, ok := val.(ProtoMarshaler)
	if !ok {
		return nil, fmt.Errorf("%T is not a ProtoMarshaler", val)
	}

	d, err := dm.MarshalProto()
	if err != nil {
		return nil, err
	}

	return proto.NewValue(d)
}

func (value Null) MarshalProto() (proto.Message, error) {
	return &proto.Null{}, nil
}

func (value Bool) MarshalProto() (proto.Message, error) {
	return &proto.Bool{Inner: bool(value)}, nil
}

func (value Int) MarshalProto() (proto.Message, error) {
	return &proto.Int{Inner: int64(value)}, nil
}

func (value String) MarshalProto() (proto.Message, error) {
	return &proto.String{Inner: string(value)}, nil
}

func (value Secret) MarshalProto() (proto.Message, error) {
	return &proto.Secret{
		Name:  value.Name,
		Value: value.secret,
	}, nil
}

func (Empty) MarshalProto() (proto.Message, error) {
	return &proto.Array{}, nil
}

func (value Pair) MarshalProto() (proto.Message, error) {
	vs, err := ToSlice(value)
	if err != nil {
		return nil, err
	}

	pvs := make([]*proto.Value, len(vs))
	for i, v := range vs {
		pvs[i], err = MarshalProto(v)
		if err != nil {
			return nil, fmt.Errorf("%d: %w", i, err)
		}
	}

	return &proto.Array{Values: pvs}, nil
}

func (value *Scope) MarshalProto() (proto.Message, error) {
	var bindings []*proto.Binding
	err := value.Each(func(sym Symbol, val Value) error {
		v, err := MarshalProto(val)
		if err != nil {
			return fmt.Errorf("%s: %w", sym, err)
		}

		bindings = append(bindings, &proto.Binding{
			Name:  string(sym),
			Value: v,
		})

		return nil
	})
	if err != nil {
		return nil, err
	}

	return &proto.Object{
		Bindings: bindings,
	}, nil
}

func (value FilePath) MarshalProto() (proto.Message, error) {
	return &proto.FilePath{
		Path: value.Path,
	}, nil
}

func (value DirPath) MarshalProto() (proto.Message, error) {
	return &proto.DirPath{
		Path: value.Path,
	}, nil
}

func (fod FileOrDirPath) MarshalProto() (proto.Message, error) {
	pv := &proto.FilesystemPath{}

	if fod.File != nil {
		f, err := fod.File.MarshalProto()
		if err != nil {
			return nil, err
		}

		pv.Path = &proto.FilesystemPath_File{File: f.(*proto.FilePath)}
	} else if fod.Dir != nil {
		d, err := fod.Dir.MarshalProto()
		if err != nil {
			return nil, err
		}

		pv.Path = &proto.FilesystemPath_Dir{Dir: d.(*proto.DirPath)}
	} else {
		return nil, fmt.Errorf("unknown type %T", fod.ToValue())
	}

	return pv, nil
}

func (value HostPath) MarshalProto() (proto.Message, error) {
	pv := &proto.HostPath{
		Context: value.ContextDir,
	}

	pathp, err := value.Path.MarshalProto()
	if err != nil {
		return nil, err
	}

	pv.Path = pathp.(*proto.FilesystemPath)

	return pv, nil
}

func (value FSPath) MarshalProto() (proto.Message, error) {
	pv := &proto.FSPath{
		Id: value.ID,
	}

	pathp, err := value.Path.MarshalProto()
	if err != nil {
		return nil, err
	}

	pv.Path = pathp.(*proto.FilesystemPath)

	return pv, nil
}

func (value Thunk) MarshalProto() (proto.Message, error) {
	thunk := &proto.Thunk{
		Insecure: value.Insecure,
	}

	if value.Image != nil {
		ti, err := value.Image.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("image: %w", err)
		}

		thunk.Image = ti.(*proto.ThunkImage)
	}

	ci, err := value.Cmd.MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("command: %w", err)
	}

	thunk.Cmd = ci.(*proto.ThunkCmd)

	for i, v := range value.Args {
		pv, err := MarshalProto(v)
		if err != nil {
			return nil, fmt.Errorf("arg %d: %w", i, err)
		}

		thunk.Args = append(thunk.Args, pv)
	}

	for i, v := range value.Stdin {
		pv, err := MarshalProto(v)
		if err != nil {
			return nil, fmt.Errorf("arg %d: %w", i, err)
		}

		thunk.Args = append(thunk.Args, pv)
	}

	if value.Env != nil {
		err := value.Env.Each(func(sym Symbol, val Value) error {
			pv, err := MarshalProto(val)
			if err != nil {
				return fmt.Errorf("%s: %w", sym, err)
			}

			thunk.Env = append(thunk.Env, &proto.Binding{
				Name:  string(sym),
				Value: pv,
			})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("env: %w", err)
		}
	}

	if value.Dir != nil {
		di, err := value.Dir.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("dir: %w", err)
		}

		thunk.Dir = di.(*proto.ThunkDir)
	}

	for _, m := range value.Mounts {
		pm, err := m.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("dir: %w", err)
		}

		thunk.Mounts = append(thunk.Mounts, pm.(*proto.ThunkMount))
	}

	if value.Labels != nil {
		err := value.Labels.Each(func(sym Symbol, val Value) error {
			lv, err := MarshalProto(val)
			if err != nil {
				return fmt.Errorf("%s: %w", sym, err)
			}

			thunk.Labels = append(thunk.Labels, &proto.Binding{
				Name:  string(sym),
				Value: lv,
			})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("labels: %w", err)
		}
	}

	return thunk, nil
}

func (value ThunkPath) MarshalProto() (proto.Message, error) {
	t, err := value.Thunk.MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("thunk: %w", err)
	}

	pv := &proto.ThunkPath{
		Thunk: t.(*proto.Thunk),
	}

	pathp, err := value.Path.MarshalProto()
	if err != nil {
		return nil, err
	}

	pv.Path = pathp.(*proto.FilesystemPath)

	return pv, nil
}

func (value CommandPath) MarshalProto() (proto.Message, error) {
	return &proto.CommandPath{
		Command: value.Command,
	}, nil
}
