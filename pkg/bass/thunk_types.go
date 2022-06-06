package bass

import (
	"fmt"

	"github.com/hashicorp/go-multierror"
	"github.com/vito/bass/pkg/proto"
)

// ThunkMount configures a mount for the thunk.
type ThunkMount struct {
	Source ThunkMountSource `json:"source"`
	Target FileOrDirPath    `json:"target"`
}

func (mount *ThunkMount) UnmarshalProto(msg proto.Message) error {
	p, ok := msg.(*proto.ThunkMount)
	if !ok {
		return fmt.Errorf("unmarshal proto: %w", DecodeError{msg, mount})
	}

	if err := mount.Source.UnmarshalProto(p.GetSource()); err != nil {
		return fmt.Errorf("unmarshal proto source: %w", err)
	}

	if err := mount.Target.UnmarshalProto(p.GetTarget()); err != nil {
		return fmt.Errorf("unmarshal proto target: %w", err)
	}

	return nil
}

func (mount ThunkMount) MarshalProto() (proto.Message, error) {
	tm := &proto.ThunkMount{}

	src, err := mount.Source.MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}

	tm.Source = src.(*proto.ThunkMountSource)

	tgt, err := mount.Target.MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}

	tm.Target = tgt.(*proto.FilesystemPath)

	return tm, nil
}

// ThunkImageRef specifies an OCI image uploaded to a registry.
type ThunkImageRef struct {
	// The platform to target; influences runtime selection.
	Platform Platform `json:"platform"`

	// A reference to an image hosted on a registry.
	Repository string `json:"repository,omitempty"`

	// An OCI image archive tarball to load.
	File *ThunkPath `json:"file,omitempty"`

	// The tag to use, either from the repository or in a multi-tag OCI archive.
	Tag string `json:"tag,omitempty"`

	// An optional digest for maximally reprodicuble builds.
	Digest string `json:"digest,omitempty"`
}

func (ref ThunkImageRef) Ref() (string, error) {
	repo := ref.Repository
	if repo == "" {
		return "", fmt.Errorf("ref does not refer to a repository")
	}

	if ref.Digest != "" {
		return fmt.Sprintf("%s@%s", repo, ref.Digest), nil
	} else if ref.Tag != "" {
		return fmt.Sprintf("%s:%s", repo, ref.Tag), nil
	} else {
		return fmt.Sprintf("%s:latest", repo), nil
	}
}

func (ref ThunkImageRef) MarshalProto() (proto.Message, error) {
	pv := &proto.ThunkImageRef{
		Platform: &proto.Platform{
			Os:   ref.Platform.OS,
			Arch: ref.Platform.Arch,
		},
	}

	if ref.Tag != "" {
		pv.Tag = &ref.Tag
	}

	if ref.Digest != "" {
		pv.Digest = &ref.Digest
	}

	if ref.File != nil {
		tp, err := ref.File.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("file: %w", err)
		}

		pv.Source = &proto.ThunkImageRef_File{
			File: tp.(*proto.ThunkPath),
		}
	} else if ref.Repository != "" {
		pv.Source = &proto.ThunkImageRef_Repository{
			Repository: ref.Repository,
		}
	}

	return pv, nil
}

// Platform configures an OCI image platform.
type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch,omitempty"`
}

func (platform *Platform) UnmarshalProto(msg proto.Message) error {
	p, ok := msg.(*proto.Platform)
	if !ok {
		return DecodeError{msg, platform}
	}

	platform.OS = p.Os
	platform.Arch = p.Arch

	return nil
}

func (platform Platform) String() string {
	str := fmt.Sprintf("os=%s", platform.OS)
	if platform.Arch != "" {
		str += fmt.Sprintf(", arch=%s", platform.Arch)
	} else {
		str += ", arch=any"
	}
	return str
}

// LinuxPlatform is the minimum configuration to select a Linux runtime.
var LinuxPlatform = Platform{
	OS: "linux",
}

// CanSelect returns true if the given platform (from a runtime) matches.
func (platform Platform) CanSelect(given Platform) bool {
	if platform.OS != given.OS {
		return false
	}

	return platform.Arch == "" || platform.Arch == given.Arch
}

type ThunkMountSource struct {
	ThunkPath *ThunkPath
	HostPath  *HostPath
	FSPath    *FSPath
	Cache     *FileOrDirPath
	Secret    *Secret
}

func (mount *ThunkMountSource) UnmarshalProto(msg proto.Message) error {
	p, ok := msg.(*proto.ThunkMountSource)
	if !ok {
		return fmt.Errorf("unmarshal proto: %w", DecodeError{msg, mount})
	}

	switch x := p.GetSource().(type) {
	case *proto.ThunkMountSource_ThunkSource:
		mount.ThunkPath = &ThunkPath{}
		return mount.ThunkPath.UnmarshalProto(x.ThunkSource)
	case *proto.ThunkMountSource_HostSource:
		mount.HostPath = &HostPath{}
		return mount.HostPath.UnmarshalProto(x.HostSource)
	case *proto.ThunkMountSource_CacheSource:
		mount.Cache = &FileOrDirPath{}
		return mount.Cache.UnmarshalProto(x.CacheSource)
	case *proto.ThunkMountSource_FsSource:
		return fmt.Errorf("TODO: fs source")
	case *proto.ThunkMountSource_SecretSource:
		mount.Secret = &Secret{}
		return mount.Secret.UnmarshalProto(x.SecretSource)
	default:
		return fmt.Errorf("unmarshal proto: unknown type: %T", x)
	}

	return nil
}

func (src ThunkMountSource) MarshalProto() (proto.Message, error) {
	pv := &proto.ThunkMountSource{}

	if src.ThunkPath != nil {
		tp, err := src.ThunkPath.MarshalProto()
		if err != nil {
			return nil, err
		}

		pv.Source = &proto.ThunkMountSource_ThunkSource{
			ThunkSource: tp.(*proto.ThunkPath),
		}
	} else if src.HostPath != nil {
		ppv, err := src.HostPath.MarshalProto()
		if err != nil {
			return nil, err
		}

		pv.Source = &proto.ThunkMountSource_HostSource{
			HostSource: ppv.(*proto.HostPath),
		}
	} else if src.FSPath != nil {
		ppv, err := src.FSPath.MarshalProto()
		if err != nil {
			return nil, err
		}

		pv.Source = &proto.ThunkMountSource_FsSource{
			FsSource: ppv.(*proto.FSPath),
		}
	} else if src.Cache != nil {
		p, err := src.Cache.MarshalProto()
		if err != nil {
			return nil, err
		}

		pv.Source = &proto.ThunkMountSource_CacheSource{
			CacheSource: &proto.CachePath{
				Id:   "", // TODO
				Path: p.(*proto.FilesystemPath),
			},
		}
	} else if src.Secret != nil {
		ppv, err := src.Secret.MarshalProto()
		if err != nil {
			return nil, err
		}

		pv.Source = &proto.ThunkMountSource_SecretSource{
			SecretSource: ppv.(*proto.Secret),
		}
	} else {
		return nil, fmt.Errorf("unexpected mount source type: %T", src.ToValue())
	}

	return pv, nil
}

var _ Decodable = &ThunkMountSource{}
var _ Encodable = ThunkMountSource{}

func (enum ThunkMountSource) ToValue() Value {
	if enum.FSPath != nil {
		val, _ := ValueOf(*enum.FSPath)
		return val
	} else if enum.HostPath != nil {
		val, _ := ValueOf(*enum.HostPath)
		return val
	} else if enum.Cache != nil {
		return enum.Cache.ToValue()
	} else if enum.Secret != nil {
		return *enum.Secret
	} else {
		val, _ := ValueOf(*enum.ThunkPath)
		return val
	}
}

func (enum *ThunkMountSource) UnmarshalJSON(payload []byte) error {
	return UnmarshalJSON(payload, enum)
}

func (enum ThunkMountSource) MarshalJSON() ([]byte, error) {
	return MarshalJSON(enum.ToValue())
}

func (enum *ThunkMountSource) FromValue(val Value) error {
	var host HostPath
	if err := val.Decode(&host); err == nil {
		enum.HostPath = &host
		return nil
	}

	var fs FSPath
	if err := val.Decode(&fs); err == nil {
		enum.FSPath = &fs
		return nil
	}

	var tp ThunkPath
	if err := val.Decode(&tp); err == nil {
		enum.ThunkPath = &tp
		return nil
	}

	var cache FileOrDirPath
	if err := val.Decode(&cache); err == nil {
		enum.Cache = &cache
		return nil
	}

	var secret Secret
	if err := val.Decode(&secret); err == nil {
		enum.Secret = &secret
		return nil
	}

	return DecodeError{
		Source:      val,
		Destination: enum,
	}
}

// ThunkImage specifies the base image of a thunk - either a reference to be
// fetched, a thunk path (e.g. of a OCI/Docker tarball), or a lower thunk to
// run.
type ThunkImage struct {
	Ref   *ThunkImageRef
	Thunk *Thunk
}

func (img *ThunkImage) UnmarshalProto(msg proto.Message) error {
	p, ok := msg.(*proto.ThunkImage)
	if !ok {
		return DecodeError{msg, img}
	}

	if p.GetRefImage() != nil {
		i := p.GetRefImage()

		img.Ref = &ThunkImageRef{}

		if err := img.Ref.Platform.UnmarshalProto(i.Platform); err != nil {
			return err
		}

		if i.GetFile() != nil {
			if err := img.Ref.File.UnmarshalProto(i.GetFile()); err != nil {
				return err
			}
		}

		img.Ref.Repository = i.GetRepository()
		img.Ref.Tag = i.GetTag()
		img.Ref.Digest = i.GetDigest()
	} else if p.GetThunkImage() != nil {
		img.Thunk = &Thunk{}

		if err := img.Thunk.UnmarshalProto(p.GetThunkImage()); err != nil {
			return err
		}
	}

	return nil
}

func (img ThunkImage) MarshalProto() (proto.Message, error) {
	ti := &proto.ThunkImage{}

	if img.Ref != nil {
		ri, err := img.Ref.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("ref: %w", err)
		}

		ti.Image = &proto.ThunkImage_RefImage{
			RefImage: ri.(*proto.ThunkImageRef),
		}
	} else if img.Thunk != nil {
		tv, err := img.Thunk.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("parent: %w", err)
		}

		ti.Image = &proto.ThunkImage_ThunkImage{
			ThunkImage: tv.(*proto.Thunk),
		}
	} else {
		return nil, fmt.Errorf("unexpected image type: %T", img.ToValue())
	}

	return ti, nil
}

func (img ThunkImage) Platform() *Platform {
	if img.Ref != nil {
		return &img.Ref.Platform
	} else {
		return img.Thunk.Platform()
	}
}

var _ Decodable = &ThunkImage{}
var _ Encodable = ThunkImage{}

func (image ThunkImage) ToValue() Value {
	if image.Ref != nil {
		val, _ := ValueOf(*image.Ref)
		return val
	} else if image.Thunk != nil {
		val, _ := ValueOf(*image.Thunk)
		return val
	} else {
		panic("empty ThunkImage or unhandled type?")
	}
}

func (image *ThunkImage) UnmarshalJSON(payload []byte) error {
	return UnmarshalJSON(payload, image)
}

func (image ThunkImage) MarshalJSON() ([]byte, error) {
	return MarshalJSON(image.ToValue())
}

func (image *ThunkImage) FromValue(val Value) error {
	var errs error

	var ref ThunkImageRef
	if err := val.Decode(&ref); err == nil {
		image.Ref = &ref
		return nil
	} else {
		errs = multierror.Append(errs, fmt.Errorf("%T: %w", val, err))
	}

	var thunk Thunk
	if err := val.Decode(&thunk); err == nil {
		image.Thunk = &thunk
		return nil
	} else {
		errs = multierror.Append(errs, fmt.Errorf("%T: %w", val, err))
	}

	return fmt.Errorf("image enum: %w", errs)
}

type ThunkCmd struct {
	Cmd   *CommandPath
	File  *FilePath
	Thunk *ThunkPath
	Host  *HostPath
	FS    *FSPath
}

func (cmd *ThunkCmd) UnmarshalProto(msg proto.Message) error {
	p, ok := msg.(*proto.ThunkCmd)
	if !ok {
		return fmt.Errorf("unmarshal proto: %w", DecodeError{msg, cmd})
	}

	var err error
	switch x := p.GetCmd().(type) {
	case *proto.ThunkCmd_CommandCmd:
		err = cmd.Cmd.UnmarshalProto(x.CommandCmd)
	case *proto.ThunkCmd_FileCmd:
		err = cmd.File.UnmarshalProto(x.FileCmd)
	case *proto.ThunkCmd_ThunkCmd:
		err = cmd.Thunk.UnmarshalProto(x.ThunkCmd)
	case *proto.ThunkCmd_HostCmd:
		err = cmd.Host.UnmarshalProto(x.HostCmd)
	case *proto.ThunkCmd_FsCmd:
		return fmt.Errorf("TODO")
	default:
		return fmt.Errorf("unhandled cmd type: %T", x)
	}

	return err
}

func (cmd ThunkCmd) MarshalProto() (proto.Message, error) {
	pv := &proto.ThunkCmd{}

	if cmd.Cmd != nil {
		cv, err := cmd.Cmd.MarshalProto()
		if err != nil {
			return nil, err
		}

		pv.Cmd = &proto.ThunkCmd_CommandCmd{
			CommandCmd: cv.(*proto.CommandPath),
		}
	} else if cmd.File != nil {
		cv, err := cmd.File.MarshalProto()
		if err != nil {
			return nil, err
		}

		pv.Cmd = &proto.ThunkCmd_FileCmd{
			FileCmd: cv.(*proto.FilePath),
		}
	} else if cmd.Thunk != nil {
		cv, err := cmd.Thunk.MarshalProto()
		if err != nil {
			return nil, err
		}

		pv.Cmd = &proto.ThunkCmd_ThunkCmd{
			ThunkCmd: cv.(*proto.ThunkPath),
		}
	} else if cmd.Host != nil {
		cv, err := cmd.Host.MarshalProto()
		if err != nil {
			return nil, err
		}

		pv.Cmd = &proto.ThunkCmd_HostCmd{
			HostCmd: cv.(*proto.HostPath),
		}
	} else if cmd.FS != nil {
		cv, err := cmd.FS.MarshalProto()
		if err != nil {
			return nil, err
		}

		pv.Cmd = &proto.ThunkCmd_FsCmd{
			FsCmd: cv.(*proto.FSPath),
		}
	} else {
		return nil, fmt.Errorf("unexpected command type: %T", cmd.ToValue())
	}

	return pv, nil
}

var _ Decodable = &ThunkCmd{}
var _ Encodable = ThunkCmd{}

func (cmd ThunkCmd) ToValue() Value {
	val, err := cmd.Inner()
	if err != nil {
		panic(err)
	}

	return val
}

func (cmd ThunkCmd) Inner() (Value, error) {
	if cmd.File != nil {
		return *cmd.File, nil
	} else if cmd.Thunk != nil {
		return *cmd.Thunk, nil
	} else if cmd.Cmd != nil {
		return *cmd.Cmd, nil
	} else if cmd.Host != nil {
		return *cmd.Host, nil
	} else if cmd.FS != nil {
		return *cmd.FS, nil
	} else {
		return nil, fmt.Errorf("no value present for thunk command: %+v", cmd)
	}
}

func (path *ThunkCmd) UnmarshalJSON(payload []byte) error {
	return UnmarshalJSON(payload, path)
}

func (path ThunkCmd) MarshalJSON() ([]byte, error) {
	val, err := path.Inner()
	if err != nil {
		return nil, err

	}
	return MarshalJSON(val)
}

func (path *ThunkCmd) FromValue(val Value) error {
	var errs error
	var file FilePath
	if err := val.Decode(&file); err == nil {
		path.File = &file
		return nil
	} else {
		errs = multierror.Append(errs, fmt.Errorf("%T: %w", file, err))
	}

	var cmd CommandPath
	if err := val.Decode(&cmd); err == nil {
		path.Cmd = &cmd
		return nil
	} else {
		errs = multierror.Append(errs, fmt.Errorf("%T: %w", cmd, err))
	}

	var wlp ThunkPath
	if err := val.Decode(&wlp); err == nil {
		if wlp.Path.File != nil {
			path.Thunk = &wlp
			return nil
		} else {
			errs = multierror.Append(errs, fmt.Errorf("%T does not point to a File", wlp))
		}
	} else {
		errs = multierror.Append(errs, fmt.Errorf("%T: %w", wlp, err))
	}

	var host HostPath
	if err := val.Decode(&host); err == nil {
		path.Host = &host
		return nil
	} else {
		errs = multierror.Append(errs, fmt.Errorf("%T: %w", file, err))
	}

	var fsp FSPath
	if err := val.Decode(&fsp); err == nil {
		path.FS = &fsp
		return nil
	} else {
		errs = multierror.Append(errs, fmt.Errorf("%T: %w", file, err))
	}

	return errs
}

type ThunkDir struct {
	Dir      *DirPath
	ThunkDir *ThunkPath
	HostDir  *HostPath
}

func (dir *ThunkDir) UnmarshalProto(msg proto.Message) error {
	p, ok := msg.(*proto.ThunkDir)
	if !ok {
		return fmt.Errorf("unmarshal proto: %w", DecodeError{msg, dir})
	}

	switch x := p.GetDir().(type) {
	case *proto.ThunkDir_LocalDir:
		dir.Dir = &DirPath{}
		return dir.Dir.UnmarshalProto(x.LocalDir)
	case *proto.ThunkDir_ThunkDir:
		dir.ThunkDir = &ThunkPath{}
		return dir.ThunkDir.UnmarshalProto(x.ThunkDir)
	case *proto.ThunkDir_HostDir:
		dir.HostDir = &HostPath{}
		return dir.HostDir.UnmarshalProto(x.HostDir)
	default:
		return fmt.Errorf("unmarshal proto: unknown type: %T", x)
	}
}

func (dir ThunkDir) MarshalProto() (proto.Message, error) {
	pv := &proto.ThunkDir{}

	if dir.Dir != nil {
		dv, err := dir.Dir.MarshalProto()
		if err != nil {
			return nil, err
		}

		pv.Dir = &proto.ThunkDir_LocalDir{
			LocalDir: dv.(*proto.DirPath),
		}
	} else if dir.ThunkDir != nil {
		cv, err := dir.ThunkDir.MarshalProto()
		if err != nil {
			return nil, err
		}

		pv.Dir = &proto.ThunkDir_ThunkDir{
			ThunkDir: cv.(*proto.ThunkPath),
		}
	} else if dir.HostDir != nil {
		cv, err := dir.HostDir.MarshalProto()
		if err != nil {
			return nil, err
		}

		pv.Dir = &proto.ThunkDir_HostDir{
			HostDir: cv.(*proto.HostPath),
		}
	} else {
		return nil, fmt.Errorf("unexpected command type: %T", dir.ToValue())
	}

	return pv, nil
}

var _ Decodable = &ThunkDir{}
var _ Encodable = ThunkDir{}

func (path ThunkDir) ToValue() Value {
	if path.ThunkDir != nil {
		return *path.ThunkDir
	} else if path.Dir != nil {
		return *path.Dir
	} else {
		return *path.HostDir
	}
}

func (path *ThunkDir) UnmarshalJSON(payload []byte) error {
	return UnmarshalJSON(payload, path)
}

func (path ThunkDir) MarshalJSON() ([]byte, error) {
	return MarshalJSON(path.ToValue())
}

func (path *ThunkDir) FromValue(val Value) error {
	var errs error

	var dir DirPath
	if err := val.Decode(&dir); err == nil {
		path.Dir = &dir
		return nil
	} else {
		errs = multierror.Append(errs, fmt.Errorf("%T: %w", dir, err))
	}

	var wlp ThunkPath
	if err := val.Decode(&wlp); err == nil {
		if wlp.Path.Dir != nil {
			path.ThunkDir = &wlp
			return nil
		} else {
			return fmt.Errorf("dir thunk path must be a directory: %s", wlp.Repr())
		}
	} else {
		errs = multierror.Append(errs, fmt.Errorf("%T: %w", wlp, err))
	}

	var hp HostPath
	if err := val.Decode(&hp); err == nil {
		if hp.Path.Dir != nil {
			path.HostDir = &hp
			return nil
		} else {
			return fmt.Errorf("dir host path must be a directory: %s", wlp.Repr())
		}
	} else {
		errs = multierror.Append(errs, fmt.Errorf("%T: %w", hp, err))
	}

	return errs
}
