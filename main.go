// The following main.go is taken from https://github.com/Luet-lab/extensions/blob/master/extensions/package-browser/main.go

package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	config "github.com/mudler/luet/pkg/config"

	installer "github.com/mudler/luet/pkg/installer"
	. "github.com/mudler/luet/pkg/logger"
	pkg "github.com/mudler/luet/pkg/package"
	"github.com/narqo/go-badge"
	"github.com/pkg/errors"
	"gopkg.in/macaron.v1"
	"gopkg.in/yaml.v2"
)

const (
	Version = "0.2"
)

var lock = &sync.Mutex{}
var Repositories installer.Repositories

func refreshRepositories(repos installer.Repositories) (installer.Repositories, error) {
	syncedRepos := installer.Repositories{}
	for _, r := range repos {
		repo, err := r.Sync(true)
		if err != nil {
			return nil, errors.Wrap(err, "Failed syncing repository: "+r.GetName())
		}
		syncedRepos = append(syncedRepos, repo)
	}

	// compute what to install and from where
	sort.Sort(syncedRepos)

	return syncedRepos, nil
}

func GetRepo(name, url, t string) (*installer.LuetSystemRepository, error) {
	if t == "" {
		t = "http"
	}
	return installer.NewLuetSystemRepositoryFromYaml([]byte(`
name: "`+name+`"
type: "`+t+`"
urls:
- "`+url+`"`), pkg.NewInMemoryDatabase(false))
}

type Repository struct {
	Name, Url, Type, Github, Description string
}
type Meta struct {
	Repositories []Repository
}

func syncRepos(repos installer.Repositories) {
	lock.Lock()
	defer lock.Unlock()
	dir, err := ioutil.TempDir(os.TempDir(), "example")
	if err != nil {
		fmt.Println("failed refreshing repository", err)
	}
	defer os.RemoveAll(dir)

	config.LuetCfg.System.TmpDirBase = dir
	config.LuetCfg.GetLogging().Color = false
	config.LuetCfg.GetGeneral().Debug = true
	InitAurora()
	repos, err = refreshRepositories(repos)
	if err != nil {
		fmt.Println("failed refreshing repository", err)
	}
	Repositories = repos
}

func main() {
	var metadata Meta
	configFile := os.Getenv("CONFIG")
	if len(configFile) == 0 {
		configFile = "config.yaml"
	}

	sleepDuration := 960
	sleepTime := os.Getenv("SLEEPTIME")
	if sleepTime != "" {
		data, err := strconv.Atoi(sleepTime)
		if err == nil {
			sleepDuration = data
		}
	}

	yamlFile, err := ioutil.ReadFile(configFile)
	if err != nil {
		panic(fmt.Sprintf("yamlFile.Get err   #%v ", err))
	}
	err = yaml.Unmarshal(yamlFile, &metadata)
	if err != nil {
		panic(fmt.Sprintf("Unmarshal err   #%v ", err))
	}

	additionalData := map[string]map[string]string{}

	repos := installer.Repositories{}
	for _, r := range metadata.Repositories {
		repo, err := GetRepo(r.Name, r.Url, r.Type)
		if err != nil {
			fmt.Println("Failed getting repo ", repo, err)
			continue
		}
		additionalData[r.Name] = make(map[string]string)
		additionalData[r.Name]["github"] = r.Github
		additionalData[r.Name]["description"] = r.Description
		additionalData[r.Name]["url"] = r.Url
		additionalData[r.Name]["type"] = r.Type
		repos = append(repos, repo)
	}

	go func() {
		for {
			syncRepos(repos)
			time.Sleep(time.Duration(sleepDuration) * time.Second)
		}
	}()

	templatesDir := os.Getenv("TEMPLATES_DIR")
	if templatesDir == "" {
		templatesDir = "/usr/share/luet-package-browser"
	}

	m := macaron.Classic()
	m.Use(macaron.Renderer(macaron.RenderOptions{
		// Directory to load templates. Default is "templates".
		Directory: templatesDir,
	}))
	// Routes
	m.Get("/:repository", func(ctx *macaron.Context) {
		lock.Lock()
		defer lock.Unlock()
		for _, r := range Repositories {
			if r.GetName() == ctx.Params(":repository") {
				packs := r.GetTree().GetDatabase().World()
				sort.SliceStable(packs, func(i, j int) bool {
					return packs[i].GetName() < packs[j].GetName()
				})
				ctx.Data["Packages"] = packs
			}
		}
		ctx.Data["AdditionalData"] = additionalData
		ctx.Data["RepositoryName"] = ctx.Params(":repository")
		ctx.HTML(200, "repository")
	})

	m.Get("/badge/:repository", func(w http.ResponseWriter, ctx *macaron.Context) {
		lock.Lock()
		defer lock.Unlock()

		var packN int
		for _, r := range Repositories {
			if r.GetName() == ctx.Params(":repository") {
				packN = len(r.GetIndex())
			}
		}
		badge, err := badge.RenderBytes(strconv.Itoa(packN), ctx.Params(":repository"), "#3C1")
		if err != nil {
			panic(err)
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Content-Length", strconv.Itoa(len(badge)))
		if _, err := w.Write(badge); err != nil {
			log.Println("unable to write image.")
		}
	})

	m.Get("/:repository/:packagecategory/:packagename", func(ctx *macaron.Context) {
		lock.Lock()
		defer lock.Unlock()
		packs := map[string][]pkg.Package{}

		for _, r := range Repositories {
			if r.GetName() == ctx.Params(":repository") {

				packages, err := r.GetTree().GetDatabase().FindPackages(&pkg.DefaultPackage{
					Name:     ctx.Params(":packagename"),
					Category: ctx.Params(":packagecategory"),
					Version:  ">=0",
				})
				if err != nil {
					fmt.Println(err)
					continue
				}
				for _, p := range packages {
					packs[r.GetName()] = append(packs[r.GetName()], p)
				}
			}
		}
		ctx.Data["PackageCategory"] = ctx.Params(":packagecategory")
		ctx.Data["PackageName"] = ctx.Params(":packagename")

		ctx.Data["Packages"] = packs

		ctx.HTML(200, "packages")
	})

	m.Get("/find/:packagecategory/:packagename", func(ctx *macaron.Context) {
		lock.Lock()
		defer lock.Unlock()
		packs := map[string][]pkg.Package{}

		for _, r := range Repositories {
			packages, err := r.GetTree().GetDatabase().FindPackages(&pkg.DefaultPackage{
				Name:     ctx.Params(":packagename"),
				Category: ctx.Params(":packagecategory"),
				Version:  ">=0",
			})
			if err != nil {
				fmt.Println(err)
				continue
			}
			for _, p := range packages {
				packs[r.GetName()] = append(packs[r.GetName()], p)
			}
		}
		ctx.Data["PackageCategory"] = ctx.Params(":packagecategory")
		ctx.Data["PackageName"] = ctx.Params(":packagename")

		ctx.Data["Packages"] = packs
		ctx.HTML(200, "packages")
	})

	m.Get("/:repository/:packagecategory/:packagename/:packageversion", func(ctx *macaron.Context) {
		lock.Lock()
		defer lock.Unlock()
		var pack pkg.Package

		find := &pkg.DefaultPackage{
			Name:     ctx.Params(":packagename"),
			Category: ctx.Params(":packagecategory"),
			Version:  ctx.Params(":packageversion"),
		}
		for _, r := range Repositories {
			if r.GetName() == ctx.Params(":repository") {
				for _, a := range r.GetIndex() {
					if a.CompileSpec.GetPackage().GetFingerPrint() == find.GetFingerPrint() {
						ctx.Data["Files"] = a.Files
						pack = a.CompileSpec.GetPackage() // We get it from compilesec which contains the build timestamp
					}
				}
			}
		}

		ctx.Data["RepositoryName"] = ctx.Params(":repository")
		ctx.Data["Package"] = pack
		ctx.HTML(200, "package")
	})

	m.Get("/", func(ctx *macaron.Context) {
		lock.Lock()
		defer lock.Unlock()

		packs := map[string][]pkg.Package{}

		for _, r := range Repositories {
			packages := r.GetTree().GetDatabase().World()
			for _, p := range packages {
				packs[r.GetName()] = append(packs[r.GetName()], p)
			}
		}

		ctx.Data["AdditionalData"] = additionalData
		ctx.Data["Packages"] = packs
		ctx.Data["Repositories"] = Repositories
		ctx.HTML(200, "index")
	})

	fmt.Printf("Starting luet-package-browser v%s\n", Version)
	m.Run()
}
