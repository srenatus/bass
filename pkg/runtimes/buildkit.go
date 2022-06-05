package runtimes

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/adrg/xdg"
	"github.com/containerd/containerd/platforms"
	"github.com/docker/distribution/reference"
	"github.com/moby/buildkit/client"
	kitdclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/dockerfile/dockerignore"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth/authprovider"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/moby/buildkit/util/entitlements"
	"github.com/morikuni/aec"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/tonistiigi/units"
	"github.com/vito/bass/pkg/bass"
	"github.com/vito/bass/pkg/cli"
	"github.com/vito/bass/pkg/ioctx"
	"github.com/vito/bass/pkg/proto"
	"github.com/vito/progrock"
	"github.com/vito/progrock/graph"
	gproto "google.golang.org/protobuf/proto"

	_ "embed"
)

const buildkitProduct = "bass"

type BuildkitConfig struct {
	DisableCache bool `json:"disable_cache,omitempty"`
}

var _ bass.Runtime = &Buildkit{}

//go:embed bin/exe.*
var shims embed.FS

const BuildkitName = "buildkit"

const shimExePath = "/bass/shim"
const workDir = "/bass/work"
const ioDir = "/bass/io"
const inputFile = "/bass/io/in"
const outputFile = "/bass/io/out"

const digestBucket = "_digests"
const configBucket = "_configs"

var allShims = map[string][]byte{}

func init() {
	RegisterRuntime(BuildkitName, NewBuildkit)

	files, err := shims.ReadDir("bin")
	if err == nil {
		for _, f := range files {
			content, err := shims.ReadFile(path.Join("bin", f.Name()))
			if err == nil {
				allShims[f.Name()] = content
			}
		}
	}
}

const BuildkitdAddrName = "buildkitd"

var DefaultBuildkitAddrs = bass.RuntimeAddrs{
	BuildkitdAddrName: nil,
}

func init() {
	// support respecting XDG_RUNTIME_DIR instead of assuming /run/
	sockPath, _ := xdg.SearchConfigFile("bass/buildkitd.sock")

	if sockPath == "" {
		sockPath, _ = xdg.SearchRuntimeFile("buildkit/buildkitd.sock")
	}

	if sockPath == "" {
		sockPath = "/run/buildkit/buildkitd.sock"
	}

	DefaultBuildkitAddrs[BuildkitdAddrName] = &url.URL{Scheme: "unix", Path: sockPath}
}

type Buildkit struct {
	Config   BuildkitConfig
	Client   *kitdclient.Client
	Platform ocispecs.Platform

	authp session.Attachable
}

func NewBuildkit(_ bass.RuntimePool, addrs bass.RuntimeAddrs, cfg *bass.Scope) (bass.Runtime, error) {
	var config BuildkitConfig
	if cfg != nil {
		if err := cfg.Decode(&config); err != nil {
			return nil, fmt.Errorf("docker runtime config: %w", err)
		}
	}

	addr, found := addrs.Service(BuildkitdAddrName)
	if !found {
		return nil, fmt.Errorf("service not configured: %s", BuildkitdAddrName)
	}

	client, err := kitdclient.New(context.TODO(), addr.String())
	if err != nil {
		return nil, fmt.Errorf("dial buildkit: %w", err)
	}

	workers, err := client.ListWorkers(context.TODO())
	if err != nil {
		return nil, fmt.Errorf("list buildkit workers: %w", err)
	}

	var platform ocispecs.Platform
	var checkSame platforms.Matcher
	for _, w := range workers {
		if checkSame != nil && !checkSame.Match(w.Platforms[0]) {
			return nil, fmt.Errorf("TODO: workers have different platforms: %s != %s", w.Platforms[0], platform)
		}

		platform = w.Platforms[0]
		checkSame = platforms.Only(platform)
	}

	return &Buildkit{
		Config:   config,
		Client:   client,
		Platform: platform,

		authp: authprovider.NewDockerAuthProvider(os.Stderr),
	}, nil
}

func (runtime *Buildkit) Resolve(ctx context.Context, imageRef *proto.ThunkImageRef) (*proto.ThunkImageRef, error) {
	ref, err := imageRef.Ref()
	if err != nil {
		// TODO: it might make sense to resolve an OCI archive ref to a digest too
		return nil, err
	}

	// convert 'ubuntu' to 'docker.io/library/ubuntu:latest'
	normalized, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return nil, fmt.Errorf("normalize ref: %w", err)
	}

	statusProxy := forwardStatus(progrock.RecorderFromContext(ctx))
	defer statusProxy.Wait()

	_, err = runtime.Client.Build(ctx, kitdclient.SolveOpt{
		Session: []session.Attachable{
			runtime.authp,
		},
	}, buildkitProduct, func(ctx context.Context, gw gwclient.Client) (*gwclient.Result, error) {
		digest, _, err := gw.ResolveImageConfig(ctx, normalized.String(), llb.ResolveImageConfigOpt{
			Platform: &runtime.Platform,
		})
		if err != nil {
			return nil, fmt.Errorf("resolve: %w", err)
		}

		imageRef.Digest = gproto.String(digest.String())

		return &gwclient.Result{}, nil
	}, statusProxy.Writer())
	if err != nil {
		return nil, statusProxy.NiceError("resolve failed", err)
	}

	return imageRef, nil
}

func (runtime *Buildkit) Run(ctx context.Context, w io.Writer, thunk *proto.Thunk) error {
	sha2, err := thunk.SHA256()
	if err != nil {
		return err
	}

	tmp, err := os.MkdirTemp("", "thunk-"+sha2)
	if err != nil {
		return err
	}

	defer os.RemoveAll(tmp)

	err = runtime.build(
		ctx,
		thunk,
		w != io.Discard,
		func(st llb.ExecState, _ string) marshalable { return st.GetMount(ioDir) },
		kitdclient.ExportEntry{
			Type:      kitdclient.ExporterLocal,
			OutputDir: tmp,
		},
	)
	if err != nil {
		return err
	}

	response, err := os.Open(filepath.Join(tmp, filepath.Base(outputFile)))
	if err == nil {
		defer response.Close()

		_, err = io.Copy(w, response)
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}
	}

	return nil
}

type marshalable interface {
	Marshal(ctx context.Context, co ...llb.ConstraintsOpt) (*llb.Definition, error)
}

func (runtime *Buildkit) Export(ctx context.Context, w io.Writer, thunk *proto.Thunk) error {
	return runtime.build(
		ctx,
		thunk,
		false,
		func(st llb.ExecState, _ string) marshalable { return st },
		kitdclient.ExportEntry{
			Type: kitdclient.ExporterOCI,
			Output: func(map[string]string) (io.WriteCloser, error) {
				return nopCloser{w}, nil
			},
		},
	)
}

func (runtime *Buildkit) ExportPath(ctx context.Context, w io.Writer, tp *proto.ThunkPath) error {
	thunk := tp.Thunk
	path := tp.Path

	return runtime.build(
		ctx,
		thunk,
		false,
		func(st llb.ExecState, sp string) marshalable {
			copyOpt := &llb.CopyInfo{}
			if tp.Path.GetDir() != nil {
				copyOpt.CopyDirContentsOnly = true
			}

			return llb.Scratch().File(
				llb.Copy(st.GetMount(workDir), filepath.Join(sp, path.FromSlash()), ".", copyOpt),
				llb.WithCustomNamef("[hide] copy %s", path.Slash()),
			)
		},
		kitdclient.ExportEntry{
			Type: kitdclient.ExporterTar,
			Output: func(map[string]string) (io.WriteCloser, error) {
				return nopCloser{w}, nil
			},
		},
	)
}

func (runtime *Buildkit) Prune(ctx context.Context, opts bass.PruneOpts) error {
	stderr := ioctx.StderrFromContext(ctx)
	tw := tabwriter.NewWriter(stderr, 2, 8, 2, ' ', 0)

	ch := make(chan kitdclient.UsageInfo)
	printed := make(chan struct{})

	total := int64(0)

	go func() {
		defer close(printed)
		for du := range ch {
			line := fmt.Sprintf("pruned %s", du.ID)
			if du.LastUsedAt != nil {
				line += fmt.Sprintf("\tuses: %d\tlast used: %s ago", du.UsageCount, time.Since(*du.LastUsedAt).Truncate(time.Second))
			}

			line += fmt.Sprintf("\tsize: %.2f", units.Bytes(du.Size))

			line += fmt.Sprintf("\t%s", aec.LightBlackF.Apply(du.Description))

			fmt.Fprintln(tw, line)

			total += du.Size
		}
	}()

	kitdOpts := []kitdclient.PruneOption{
		client.WithKeepOpt(opts.KeepDuration, opts.KeepBytes),
	}

	if opts.All {
		kitdOpts = append(kitdOpts, client.PruneAll)
	}

	err := runtime.Client.Prune(ctx, ch, kitdOpts...)
	close(ch)
	<-printed
	if err != nil {
		return err
	}

	fmt.Fprintf(tw, "total: %.2f\n", units.Bytes(total))

	return tw.Flush()
}

func (runtime *Buildkit) Close() error {
	return runtime.Client.Close()
}

func (runtime *Buildkit) build(ctx context.Context, thunk *proto.Thunk, captureStdout bool, transform func(llb.ExecState, string) marshalable, exports ...kitdclient.ExportEntry) error {
	var def *llb.Definition
	var secrets map[string][]byte
	var localDirs map[string]string
	var allowed []entitlements.Entitlement

	statusProxy := forwardStatus(progrock.RecorderFromContext(ctx))
	defer statusProxy.Wait()

	// build llb definition using the remote gateway for image resolution
	_, err := runtime.Client.Build(ctx, kitdclient.SolveOpt{
		Session: []session.Attachable{runtime.authp},
	}, buildkitProduct, func(ctx context.Context, gw gwclient.Client) (*gwclient.Result, error) {
		b := runtime.newBuilder(gw)

		st, sp, needsInsecure, err := b.llb(ctx, thunk, captureStdout)
		if err != nil {
			return nil, err
		}

		if needsInsecure {
			allowed = append(allowed, entitlements.EntitlementSecurityInsecure)
		}

		localDirs = b.localDirs
		secrets = b.secrets

		def, err = transform(st, sp).Marshal(ctx)
		if err != nil {
			return nil, err
		}

		return &gwclient.Result{}, nil
	}, statusProxy.Writer())
	if err != nil {
		return statusProxy.NiceError("llb build failed", err)
	}

	_, err = runtime.Client.Solve(ctx, def, kitdclient.SolveOpt{
		LocalDirs:           localDirs,
		AllowedEntitlements: allowed,
		Session: []session.Attachable{
			runtime.authp,
			secretsprovider.FromMap(secrets),
		},
		Exports: exports,
	}, statusProxy.Writer())
	if err != nil {
		return statusProxy.NiceError("build failed", err)
	}

	return nil
}

type builder struct {
	runtime  *Buildkit
	resolver llb.ImageMetaResolver

	secrets   map[string][]byte
	localDirs map[string]string
}

func (runtime *Buildkit) newBuilder(resolver llb.ImageMetaResolver) *builder {
	return &builder{
		runtime:  runtime,
		resolver: resolver,

		secrets:   map[string][]byte{},
		localDirs: map[string]string{},
	}
}

func (b *builder) llb(ctx context.Context, thunk *proto.Thunk, captureStdout bool) (llb.ExecState, string, bool, error) {
	cmd, err := NewCommand(thunk)
	if err != nil {
		return llb.ExecState{}, "", false, err
	}

	imageRef, runState, sourcePath, needsInsecure, err := b.imageRef(ctx, thunk.Image)
	if err != nil {
		return llb.ExecState{}, "", false, err
	}

	id, err := thunk.SHA256()
	if err != nil {
		return llb.ExecState{}, "", false, err
	}

	cmdPayload, err := bass.MarshalJSON(cmd)
	if err != nil {
		return llb.ExecState{}, "", false, err
	}

	shimExe, err := b.shim()
	if err != nil {
		return llb.ExecState{}, "", false, err
	}

	runOpt := []llb.RunOption{
		llb.WithCustomName(thunk.Cmdline()),
		// NB: this is load-bearing; it's what busts the cache with different labels
		llb.Hostname(id),
		llb.AddMount("/tmp", llb.Scratch(), llb.Tmpfs()),
		llb.AddMount("/dev/shm", llb.Scratch(), llb.Tmpfs()),
		llb.AddMount(ioDir, llb.Scratch().File(
			llb.Mkfile("in", 0600, cmdPayload),
			llb.WithCustomName("[hide] mount command json"),
		)),
		llb.AddMount(shimExePath, shimExe, llb.SourcePath("run")),
		llb.With(llb.Dir(workDir)),
		llb.Args([]string{shimExePath, "run", inputFile}),
	}

	if captureStdout {
		runOpt = append(runOpt, llb.AddEnv("_BASS_OUTPUT", outputFile))
	}

	if thunk.Insecure {
		needsInsecure = true

		runOpt = append(runOpt,
			llb.WithCgroupParent(id),
			llb.Security(llb.SecurityModeInsecure))
	}

	var remountedWorkdir bool
	for _, mount := range cmd.Mounts {
		var targetPath string
		if filepath.IsAbs(mount.Target) {
			targetPath = mount.Target
		} else {
			targetPath = filepath.Join(workDir, mount.Target)
		}

		mountOpt, sp, ni, err := b.initializeMount(ctx, mount.Source, targetPath)
		if err != nil {
			return llb.ExecState{}, "", false, err
		}

		if targetPath == workDir {
			remountedWorkdir = true
			sourcePath = sp
		}

		if ni {
			needsInsecure = true
		}

		runOpt = append(runOpt, mountOpt)
	}

	if !remountedWorkdir {
		if sourcePath != "" {
			// NB: could just call SourcePath with "", but this is to ensure there's
			// code coverage
			runOpt = append(runOpt, llb.AddMount(workDir, runState, llb.SourcePath(sourcePath)))
		} else {
			runOpt = append(runOpt, llb.AddMount(workDir, runState))
		}
	}

	if b.runtime.Config.DisableCache {
		runOpt = append(runOpt, llb.IgnoreCache)
	}

	return imageRef.Run(runOpt...), sourcePath, needsInsecure, nil
}

func (b *builder) shim() (llb.State, error) {
	shimExe, found := allShims["exe."+b.runtime.Platform.Architecture]
	if !found {
		return llb.State{}, fmt.Errorf("no shim found for %s", b.runtime.Platform.Architecture)
	}

	return llb.Scratch().File(
		llb.Mkfile("/run", 0755, shimExe),
		llb.WithCustomName("[hide] load bass shim"),
	), nil
}

func (b *builder) imageRef(ctx context.Context, image *proto.ThunkImage) (llb.State, llb.State, string, bool, error) {
	if image == nil {
		// TODO: test
		return llb.Scratch(), llb.Scratch(), "", false, nil
	}

	if image.GetRefImage() != nil {
		refImage := image.GetRefImage()
		if refImage.GetFile() != nil {
			return b.unpackImageArchive(ctx, refImage.GetFile(), refImage.GetTag())
		}

		ref, err := refImage.Ref()
		if err != nil {
			return llb.State{}, llb.State{}, "", false, err
		}

		return llb.Image(
			ref,
			llb.WithMetaResolver(b.resolver),
			llb.Platform(b.runtime.Platform),
		), llb.Scratch(), "", false, nil
	}

	if image.GetThunkImage() != nil {
		execState, sourcePath, needsInsecure, err := b.llb(ctx, image.GetThunkImage(), false)
		if err != nil {
			return llb.State{}, llb.State{}, "", false, fmt.Errorf("image thunk llb: %w", err)
		}

		return execState.State, execState.GetMount(workDir), sourcePath, needsInsecure, nil
	}

	return llb.State{}, llb.State{}, "", false, fmt.Errorf("unsupported image type: %+v", image)
}

func (b *builder) unpackImageArchive(ctx context.Context, thunkPath *proto.ThunkPath, tag string) (llb.State, llb.State, string, bool, error) {
	shimExe, err := b.shim()
	if err != nil {
		return llb.State{}, llb.State{}, "", false, err
	}

	thunkSt, baseSourcePath, needsInsecure, err := b.llb(ctx, thunkPath.Thunk, false)
	if err != nil {
		return llb.State{}, llb.State{}, "", false, fmt.Errorf("thunk llb: %w", err)
	}

	sourcePath := filepath.Join(baseSourcePath, thunkPath.Path.FromSlash())

	configSt := llb.Scratch().Run(
		llb.AddMount("/shim", shimExe, llb.SourcePath("run")),
		llb.AddMount(
			"/image.tar",
			thunkSt.GetMount(workDir),
			llb.SourcePath(sourcePath),
		),
		llb.AddMount("/config", llb.Scratch()),
		llb.Args([]string{"/shim", "get-config", "/image.tar", tag, "/config"}),
	)

	unpackSt := llb.Scratch().Run(
		llb.AddMount("/shim", shimExe, llb.SourcePath("run")),
		llb.AddMount(
			"/image.tar",
			thunkSt.GetMount(workDir),
			llb.SourcePath(sourcePath),
		),
		llb.AddMount("/rootfs", llb.Scratch()),
		llb.Args([]string{"/shim", "unpack", "/image.tar", tag, "/rootfs"}),
	)

	image := unpackSt.GetMount("/rootfs")

	var allowed []entitlements.Entitlement
	if needsInsecure {
		allowed = append(allowed, entitlements.EntitlementSecurityInsecure)
	}

	statusProxy := forwardStatus(progrock.RecorderFromContext(ctx))
	defer statusProxy.Wait()

	_, err = b.runtime.Client.Build(ctx, kitdclient.SolveOpt{
		LocalDirs:           b.localDirs,
		AllowedEntitlements: allowed,
		Session: []session.Attachable{
			b.runtime.authp,
			secretsprovider.FromMap(b.secrets),
		},
	}, buildkitProduct, func(ctx context.Context, gw gwclient.Client) (*gwclient.Result, error) {
		def, err := configSt.GetMount("/config").Marshal(ctx, llb.WithCaps(gw.BuildOpts().LLBCaps))
		if err != nil {
			return nil, err
		}

		res, err := gw.Solve(ctx, gwclient.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, err
		}

		singleRef, err := res.SingleRef()
		if err != nil {
			return nil, fmt.Errorf("get single ref: %w", err)
		}

		cfg, err := singleRef.ReadFile(ctx, gwclient.ReadRequest{Filename: "/config.json"})
		if err != nil {
			return nil, fmt.Errorf("read config.json: %w", err)
		}

		var iconf ocispecs.ImageConfig
		err = json.Unmarshal(cfg, &iconf)
		if err != nil {
			return nil, fmt.Errorf("unmarshal runtime config: %w", err)
		}

		for _, env := range iconf.Env {
			parts := strings.SplitN(env, "=", 2)
			if len(parts[0]) > 0 {
				var v string
				if len(parts) > 1 {
					v = parts[1]
				}
				image = image.AddEnv(parts[0], v)
			}
		}

		return &gwclient.Result{}, nil
	}, statusProxy.Writer())
	if err != nil {
		return llb.State{}, llb.State{}, "", false, statusProxy.NiceError("oci unpack failed", err)
	}

	return image, llb.Scratch(), "", needsInsecure, nil
}

func (b *builder) initializeMount(ctx context.Context, source *proto.ThunkMountSource, targetPath string) (llb.RunOption, string, bool, error) {
	switch x := source.GetSource().(type) {
	case *proto.ThunkMountSource_ThunkSource:
		thunkPath := x.ThunkSource

		thunkSt, baseSourcePath, needsInsecure, err := b.llb(ctx, thunkPath.Thunk, false)
		if err != nil {
			return nil, "", false, fmt.Errorf("thunk llb: %w", err)
		}

		sourcePath := filepath.Join(baseSourcePath, thunkPath.Path.FromSlash())

		return llb.AddMount(
			targetPath,
			thunkSt.GetMount(workDir),
			llb.SourcePath(sourcePath),
		), sourcePath, needsInsecure, nil
	case *proto.ThunkMountSource_HostSource:
		hostPath := x.HostSource

		contextDir := hostPath.Context
		b.localDirs[contextDir] = hostPath.Context

		var excludes []string
		ignorePath := filepath.Join(contextDir, ".bassignore")
		ignore, err := os.Open(ignorePath)
		if err == nil {
			excludes, err = dockerignore.ReadAll(ignore)
			if err != nil {
				return nil, "", false, fmt.Errorf("parse %s: %w", ignorePath, err)
			}
		}

		sourcePath := hostPath.Path.FromSlash()

		return llb.AddMount(
			targetPath,
			llb.Scratch().File(llb.Copy(
				llb.Local(
					contextDir,
					llb.ExcludePatterns(excludes),
					llb.Differ(llb.DiffMetadata, false),
				),
				sourcePath, // allow fine-grained caching control
				sourcePath,
				&llb.CopyInfo{
					CopyDirContentsOnly: true,
					CreateDestPath:      true,
				},
			)),
			llb.SourcePath(sourcePath),
		), sourcePath, false, nil
	// case *proto.ThunkMountSource_FsSource:
	// 	fsp := x.FsSource

	// 	sourcePath := fsp.Path.FromSlash()

	// 	if fsp.Path.GetFile() != nil {
	// 		content, err := fs.ReadFile(fsp.FS, path.Clean(fsp.Path.Slash()))
	// 		if err != nil {
	// 			return nil, "", false, err
	// 		}

	// 		tree := llb.Scratch()

	// 		filePath := path.Clean(fsp.Path.String())
	// 		if strings.Contains(filePath, "/") {
	// 			tree = tree.File(llb.Mkdir(path.Dir(filePath), 0755, llb.WithParents(true)))
	// 		}

	// 		return llb.AddMount(
	// 			targetPath,
	// 			tree.File(llb.Mkfile(filePath, 0644, content)),
	// 			llb.SourcePath(sourcePath),
	// 		), sourcePath, false, nil
	// 	} else {
	// 		tree := llb.Scratch()

	// 		err := fs.WalkDir(fsp.FS, path.Clean(fsp.Path.Slash()), func(walkPath string, d fs.DirEntry, err error) error {
	// 			if err != nil {
	// 				return err
	// 			}

	// 			info, err := d.Info()
	// 			if err != nil {
	// 				return err
	// 			}

	// 			if d.IsDir() {
	// 				tree = tree.File(llb.Mkdir(walkPath, info.Mode(), llb.WithParents(true)))
	// 			} else {
	// 				content, err := fs.ReadFile(fsp.FS, walkPath)
	// 				if err != nil {
	// 					return fmt.Errorf("read %s: %w", walkPath, err)
	// 				}

	// 				if strings.Contains(walkPath, "/") {
	// 					tree = tree.File(
	// 						llb.Mkdir(path.Dir(walkPath), 0755, llb.WithParents(true)),
	// 					)
	// 				}

	// 				tree = tree.File(llb.Mkfile(walkPath, info.Mode(), content))
	// 			}

	// 			return nil
	// 		})
	// 		if err != nil {
	// 			return nil, "", false, fmt.Errorf("walk %s: %w", fsp, err)
	// 		}

	// 		return llb.AddMount(
	// 			targetPath,
	// 			tree,
	// 			llb.SourcePath(sourcePath),
	// 		), sourcePath, false, nil
	// 	}
	case *proto.ThunkMountSource_CacheSource:
		return llb.AddMount(
			targetPath,
			llb.Scratch(),
			// TODO: use id, and use path as subpath
			llb.AsPersistentCacheDir(x.CacheSource.Path.Slash(), llb.CacheMountLocked),
		), "", false, nil
	case *proto.ThunkMountSource_SecretSource:
		secret := x.SecretSource
		id := secret.Name
		b.secrets[id] = secret.Value
		return llb.AddSecret(targetPath, llb.SecretID(id)), "", false, nil
	default:
		return nil, "", false, fmt.Errorf("unhandled mount source: %T", source)
	}
}

func hash(s string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(s)))
}

type nopCloser struct {
	io.Writer
}

func (nopCloser) Close() error { return nil }

func forwardStatus(rec *progrock.Recorder) *statusProxy {
	return &statusProxy{
		rec:  rec,
		wg:   new(sync.WaitGroup),
		prog: cli.NewProgress(),
	}
}

// a bit of a cludge; this translates buildkit progress messages to progrock
// status messages, and also records the progress so that we can emit it in a
// friendlier error message
type statusProxy struct {
	rec  *progrock.Recorder
	wg   *sync.WaitGroup
	prog *cli.Progress
}

func (proxy *statusProxy) proxy(rec *progrock.Recorder, statuses chan *kitdclient.SolveStatus) {
	for {
		s, ok := <-statuses
		if !ok {
			break
		}

		vs := make([]*graph.Vertex, len(s.Vertexes))
		for i, v := range s.Vertexes {
			// TODO: we have strayed from upstream Buildkit, and it's tricky to
			// un-stray because now there are fields coupled to Buildkit types.
			vs[i] = &graph.Vertex{
				Digest:    v.Digest,
				Inputs:    v.Inputs,
				Name:      v.Name,
				Started:   v.Started,
				Completed: v.Completed,
				Cached:    v.Cached,
				Error:     v.Error,
			}
		}

		ss := make([]*graph.VertexStatus, len(s.Statuses))
		for i, s := range s.Statuses {
			ss[i] = (*graph.VertexStatus)(s)
		}

		ls := make([]*graph.VertexLog, len(s.Logs))
		for i, l := range s.Logs {
			ls[i] = (*graph.VertexLog)(l)
		}

		gstatus := &graph.SolveStatus{
			Vertexes: vs,
			Statuses: ss,
			Logs:     ls,
		}

		proxy.prog.WriteStatus(gstatus)
		rec.Record(gstatus)
	}
}

func (proxy *statusProxy) Writer() chan *kitdclient.SolveStatus {
	statuses := make(chan *kitdclient.SolveStatus)

	proxy.wg.Add(1)
	go func() {
		defer proxy.wg.Done()
		proxy.proxy(proxy.rec, statuses)
	}()

	return statuses
}

func (proxy *statusProxy) Wait() {
	proxy.wg.Wait()
}

func (proxy *statusProxy) NiceError(msg string, err error) bass.NiceError {
	return proxy.prog.WrapError(msg, err)
}
