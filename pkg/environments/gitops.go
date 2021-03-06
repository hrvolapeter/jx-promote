package environments

import (
	"fmt"
	"io/ioutil"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/ghodss/yaml"
	"github.com/jenkins-x/go-scm/scm"
	jenkinsio "github.com/jenkins-x/jx-api/pkg/apis/jenkins.io"
	"github.com/jenkins-x/jx-apps/pkg/jxapps"
	"github.com/jenkins-x/jx-helpers/pkg/files"
	"github.com/jenkins-x/jx-helpers/pkg/gitclient"
	"github.com/jenkins-x/jx-helpers/pkg/stringhelpers"
	"github.com/jenkins-x/jx-helpers/pkg/termcolor"
	"github.com/jenkins-x/jx-promote/pkg/apis/promote/v1alpha1"

	"github.com/pkg/errors"

	"k8s.io/helm/pkg/proto/hapi/chart"

	jenkinsv1 "github.com/jenkins-x/jx-api/pkg/apis/jenkins.io/v1"
	helm "github.com/jenkins-x/jx-helpers/pkg/helmer"
	"github.com/jenkins-x/jx-logging/pkg/log"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	helmchart "k8s.io/helm/pkg/proto/hapi/chart"
)

const (
	// LabelUpdatebot is the label applied to PRs created by updatebot
	LabelUpdatebot = "updatebot"
)

// Create a pull request against the environment repository for env.
// The EnvironmentPullRequestOptions are used to provide a Gitter client for performing git operations,
// a GitProvider client for talking to the git provider,
// a callback ModifyChartFn which is where the changes you want to make are defined.
// The branchNameText defines the branch name used, the title is used for both the commit and the pull request title,
// the message as the body for both the commit and the pull request,
// and the pullRequestInfo for any existing PR that exists to modify the environment that we want to merge these
// changes into.
func (o *EnvironmentPullRequestOptions) Create(env *jenkinsv1.Environment, prDir string, pullRequestDetails *scm.PullRequest, chartName string, autoMerge bool) (*scm.PullRequest, error) {
	if prDir == "" {
		tempDir, err := ioutil.TempDir("", "create-pr")
		if err != nil {
			return nil, err
		}
		prDir = tempDir
		defer os.RemoveAll(tempDir)
	}

	gitURL := env.Spec.Source.URL
	dir, err := gitclient.CloneToDir(o.Gitter, gitURL, "")
	if err != nil {
		return nil, errors.Wrapf(err, "failed to clone environment %s URL %s", env.Spec.Label, gitURL)
	}

	o.OutDir = dir
	log.Logger().Infof("cloned %s to %s", termcolor.ColorInfo(gitURL), termcolor.ColorInfo(dir))

	// TODO fork if needed?
	currentSha, err := gitclient.GetLatestCommitSha(o.Gitter, dir)
	if err != nil {
		return nil, errors.Wrap(err, "could not get current commit sha")
	}

	if o.Function == nil {
		return nil, errors.Errorf("no change function configured")
	}
	err = o.Function()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to invoke change function in dir %s", dir)
	}

	labels := make([]string, 0)
	labels = append(labels, o.Labels...)
	if autoMerge {
		value := LabelUpdatebot
		contains := false
		for _, l := range pullRequestDetails.Labels {
			if l != nil {
				if l.Name == value {
					contains = true
					break
				}
			}
		}
		if !contains {
			pullRequestDetails.Labels = append(pullRequestDetails.Labels, &scm.Label{
				Name: value,
			})
		}
	}

	latestSha, err := gitclient.GetLatestCommitSha(o.Gitter, dir)
	if err != nil {
		return nil, errors.Wrap(err, "could not get current latest commit sha")
	}

	doneCommit := true
	if latestSha != currentSha {
		changed, err := gitclient.HasChanges(o.Gitter, dir)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to detect changes in dir %s", dir)
		}
		if !changed {
			// lets avoid failing to create the PR as we really have made changes
			doneCommit = false
		}
	}

	prInfo, err := o.CreatePullRequest(dir, gitURL, o.GitKind, doneCommit)
	if err != nil {
		return prInfo, errors.Wrapf(err, "failed to create pull request in dir %s", dir)
	}
	return prInfo, nil
}

// ModifyChartFiles modifies the chart files in the given directory using the given modify function
/* TODO
func ModifyChartFiles(dir string, details *scm.PullRequest, modifyFn ModifyChartFn, chartName string) error {
	requirementsFile, err := helm.FindRequirementsFileName(dir)
	if err != nil {
		return err
	}
	requirements, err := helm.LoadRequirementsFile(requirementsFile)
	if err != nil {
		return err
	}

	chartFile, err := helm.FindChartFileName(dir)
	if err != nil {
		return err
	}

	chart, err := helm.LoadChartFile(chartFile)
	if err != nil {
		return err
	}

	valuesFile, err := helm.FindValuesFileNameForChart(dir, chartName)
	if err != nil {
		return err
	}

	values, err := helm.LoadValuesFile(valuesFile)
	if err != nil {
		return err
	}

	templatesDir, err := helm.FindTemplatesDirName(dir)
	if err != nil {
		return err
	}
	templates, err := helm.LoadTemplatesDir(templatesDir)
	if err != nil {
		return err
	}

	// lets pass in the folder containing the `Chart.yaml` which is the `env` dir in GitOps management
	chartDir, _ := filepath.Split(chartFile)

	err = modifyFn(requirements, chart, values, templates, chartDir, details)
	if err != nil {
		return err
	}

	err = helm.SaveFile(requirementsFile, requirements)
	if err != nil {
		return err
	}

	err = helm.SaveFile(chartFile, chart)
	if err != nil {
		return err
	}
	return nil
}
*/

// ModifyKptFiles modifies the kpt files in the given directory using the given modify function
func ModifyKptFiles(dir string, promoteConfig *v1alpha1.Promote, details *scm.PullRequest, modifyFn ModifyKptFn) error {
	err := modifyFn(dir, promoteConfig, details)
	if err != nil {
		return err
	}
	return nil
}

// ModifyAppsFile modifies the 'jx-apps.yml' file to add/update/remove apps
func ModifyAppsFile(dir string, details *scm.PullRequest, modifyFn ModifyAppsFn) (bool, error) {
	appsConfig, fileName, err := jxapps.LoadAppConfig(dir)
	if fileName == "" {
		// if we don't have a `jx-apps.yml` then just return immediately
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = modifyFn(appsConfig, dir, details)
	if err != nil {
		return false, err
	}

	err = appsConfig.SaveConfig(fileName)
	if err != nil {
		return false, err
	}
	return true, nil
}

// CreateUpgradeRequirementsFn creates the ModifyChartFn that upgrades the requirements of a chart.
// Either all requirements may be upgraded, or the chartName,
// alias and version can be specified. A username and password can be passed for a protected repository.
// The passed inspectChartFunc will be called whilst the chart for each requirement is unpacked on the disk.
// Operations are carried out using the helmer interface and there will be more logging if verbose is true.
// The passed valuesFiles are used to add a values.yaml to each requirement.
func CreateUpgradeRequirementsFn(all bool, chartName string, alias string, version string, username string,
	password string, helmer helm.Helmer, inspectChartFunc func(chartDir string,
		existingValues map[string]interface{}) error, verbose bool, valuesFiles *ValuesFiles) ModifyChartFn {
	upgraded := false
	return func(requirements *helm.Requirements, metadata *chart.Metadata, values map[string]interface{},
		templates map[string]string, envDir string, details *scm.PullRequest) error {

		// Work through the upgrades
		for _, d := range requirements.Dependencies {
			// We need to ignore the platform unless the chart name is the platform
			upgrade := false
			if all {
				if d.Name != "jenkins-x-platform" {
					upgrade = true
				}
			} else {
				if d.Name == chartName && (d.Alias == "" || d.Alias == alias) {
					upgrade = true
				}
			}
			if upgrade {
				upgraded = true

				oldVersion := d.Version
				err := helm.InspectChart(d.Name, version, d.Repository, username, password, helmer,
					func(chartDir string) error {
						if all || version == "" {
							// Upgrade to the latest version
							_, chartVersion, err := helm.LoadChartNameAndVersion(filepath.Join(chartDir, "Chart.yaml"))
							if err != nil {
								return errors.Wrapf(err, "error loading chart from %s", chartDir)
							}
							version = chartVersion
							if verbose {
								log.Logger().Infof("No version specified so using latest version which is %s", termcolor.ColorInfo(version))
							}
						}

						err := inspectChartFunc(chartDir, values)
						if err != nil {
							return errors.Wrapf(err, "running inspectChartFunc for %s", d.Name)
						}
						err = CreateNestedRequirementDir(envDir, chartName, chartDir, version, d.Repository, verbose,
							valuesFiles, helmer)
						if err != nil {
							return errors.Wrapf(err, "creating nested app dir in chart dir %s", chartDir)
						}
						return nil
					})
				if err != nil {
					return errors.Wrapf(err, "inspecting chart %s", d.Name)
				}

				// Do the upgrade
				d.Version = version
				if !all {
					details.Title = fmt.Sprintf("Upgrade %s to %s", chartName, version)
					details.Body = fmt.Sprintf("Upgrade %s from %s to %s", chartName, oldVersion, version)
				} else {
					details.Body = fmt.Sprintf("%s\n* %s from %s to %s", details.Body, d.Name, oldVersion, version)
				}
			}
		}
		if !upgraded {
			log.Logger().Infof("No upgrades available")
		}
		return nil
	}
}

// CreateUpgradeAppConfigFn creates the ModifyAppsFn that upgrades the requirements of a chart.
// Either all requirements may be upgraded, or the chartName,
// alias and version can be specified.
func CreateUpgradeAppConfigFn(all bool, chartName string, version string) ModifyAppsFn {
	upgraded := false
	return func(appsConfig *jxapps.AppConfig, dir string, details *scm.PullRequest) error {

		// Work through the upgrades
		for _, d := range appsConfig.Apps {
			// We need to ignore the platform unless the chart name is the platform
			upgrade := false
			if all {
				upgrade = true
			} else {
				if d.Name == chartName {
					upgrade = true
				}
			}
			if upgrade {
				upgraded = true
				if d.Version != version {
					oldVersion := d.Version
					// Do the upgrade
					d.Version = version
					if !all {
						details.Title = fmt.Sprintf("Upgrade %s to %s", chartName, version)
						details.Body = fmt.Sprintf("Upgrade %s from %s to %s", chartName, oldVersion, version)
					} else {
						details.Body = fmt.Sprintf("%s\n* %s from %s to %s", details.Body, d.Name, oldVersion, version)
					}
				} else {
					upgraded = false
				}
			}
		}
		if !upgraded {
			log.Logger().Infof("No upgrades available")
		}
		return nil
	}
}

// CreateAddRequirementFn create the ModifyChartFn that adds a dependency to a chart. It takes the chart name,
// an alias for the chart, the version of the chart, the repo to load the chart from,
// valuesFiles (an array of paths to values.yaml files to add). The chartDir is the unpacked chart being added,
// which is used to add extra metadata about the chart (e.g. the charts readme, the release.yaml, the git repo url and
// the release notes) - if this points to a non-existent directory it will be ignored.
func CreateAddRequirementFn(chartName string, alias string, version string, repo string,
	valuesFiles *ValuesFiles, chartDir string, verbose bool, helmer helm.Helmer) ModifyChartFn {
	return func(requirements *helm.Requirements, chart *helmchart.Metadata, values map[string]interface{},
		templates map[string]string, envDir string, details *scm.PullRequest) error {
		// See if the chart already exists in requirements
		found := false
		for _, d := range requirements.Dependencies {
			if d.Name == chartName && d.Alias == alias {
				// App found
				log.Logger().Infof("App %s already installed.", termcolor.ColorWarning(chartName))
				if version != d.Version {
					log.Logger().Infof("To upgrade the chartName use %s or %s",
						termcolor.ColorInfo("jx upgrade chartName <chartName>"),
						termcolor.ColorInfo("jx upgrade apps --all"))
				}
				found = true
				break
			}
		}
		// If chartName not found, add it
		if !found {
			requirements.Dependencies = append(requirements.Dependencies, &helm.Dependency{
				Alias:      alias,
				Repository: repo,
				Name:       chartName,
				Version:    version,
			})
			err := CreateNestedRequirementDir(envDir, chartName, chartDir, version, repo, verbose, valuesFiles, helmer)
			if err != nil {
				return errors.Wrapf(err, "creating nested app dir in chart dir %s", chartDir)
			}

		}
		return nil
	}
}

// CreateAddAppConfigFn create the ModifyAppsFn that adds an app to the AppConfig
func CreateAddAppConfigFn(chartName string, version string, repo string) ModifyAppsFn {
	return func(appsConfig *jxapps.AppConfig, dir string, pullRequestDetails *scm.PullRequest) error {
		// See if the chart already exists in config
		found := false
		for _, d := range appsConfig.Apps {
			if d.Name == chartName {
				// App found
				log.Logger().Infof("App %s already installed.", termcolor.ColorWarning(chartName))
				if version != d.Version {
					log.Logger().Infof("To upgrade the chartName use %s or %s",
						termcolor.ColorInfo("jx upgrade chartName <chartName>"),
						termcolor.ColorInfo("jx upgrade apps --all"))
				}
				found = true
				break
			}
		}

		// If app not found, add it
		if !found {
			appsConfig.Apps = append(appsConfig.Apps, jxapps.App{
				Name: chartName,
			})
			// TODO lets default the namespace by looking up the configuration in the version stream
		}
		return nil
	}
}

// CreateNestedRequirementDir creates the a directory for a chart being added as a requirement, adding a README.md,
// the release.yaml, and the values.yaml. The dir is the unpacked chart directory to which the requirement is being
// added. The requirementName, requirementVersion,
// requirementRepository and requirementValuesFiles are used to construct the metadata,
// as well as info in the requirementDir which points to the unpacked chart of the requirement.
func CreateNestedRequirementDir(dir string, requirementName string, requirementDir string, requirementVersion string,
	requirementRepository string, verbose bool, requirementValuesFiles *ValuesFiles, helmer helm.Helmer) error {
	appDir := filepath.Join(dir, requirementName)
	rootValuesFileName := filepath.Join(appDir, helm.ValuesFileName)
	err := os.MkdirAll(appDir, 0700)
	if err != nil {
		return errors.Wrapf(err, "cannot create requirementName directory %s", appDir)
	}
	if verbose {
		log.Logger().Infof("Using %s for requirementName files", appDir)
	}
	if requirementValuesFiles != nil && len(requirementValuesFiles.Items) > 0 {
		if len(requirementValuesFiles.Items) == 1 {
			// We need to write the values file into the right spot for the requirementName
			err = files.CopyFile(requirementValuesFiles.Items[0], rootValuesFileName)
			if err != nil {
				return errors.Wrapf(err, "cannot copy values."+
					"yaml to %s directory %s", requirementName, appDir)
			}
		} else {
			var sb strings.Builder
			for _, fileName := range requirementValuesFiles.Items {
				data, err := ioutil.ReadFile(fileName)
				if err != nil {
					return errors.Wrapf(err, "failed to load values.yaml file %s", fileName)
				}
				_, err = sb.Write(data)
				if err != nil {
					return errors.Wrapf(err, "failed to append values.yaml file %s to buffer", fileName)
				}
				if !strings.HasSuffix(sb.String(), "\n") {
					sb.WriteString("\n")
				}
			}
			err = ioutil.WriteFile(rootValuesFileName, []byte(sb.String()), files.DefaultFileWritePermissions)
			if err != nil {
				return errors.Wrapf(err, "failed to write values.yaml file %s", rootValuesFileName)
			}
		}
		if verbose {
			log.Logger().Infof("Writing values file to %s", rootValuesFileName)
		}
	}
	// Write the release.yaml
	var gitRepo, releaseNotesURL, appReadme, description string
	templatesDir := filepath.Join(requirementDir, "templates")
	if _, err := os.Stat(templatesDir); os.IsNotExist(err) {
		if verbose {
			log.Logger().Infof("No templates directory exists in %s", termcolor.ColorInfo(dir))
		}
	} else if err != nil {
		return errors.Wrapf(err, "stat directory %s", appDir)
	} else {
		releaseYamlPath := filepath.Join(templatesDir, "release.yaml")
		if _, err := os.Stat(releaseYamlPath); err == nil {
			bytes, err := ioutil.ReadFile(releaseYamlPath)
			if err != nil {
				return errors.Wrapf(err, "release.yaml from %s", templatesDir)
			}
			release := jenkinsv1.Release{}
			err = yaml.Unmarshal(bytes, &release)
			if err != nil {
				return errors.Wrapf(err, "unmarshal %s", releaseYamlPath)
			}
			gitRepo = release.Spec.GitHTTPURL
			releaseNotesURL = release.Spec.ReleaseNotesURL
			releaseYamlOutPath := filepath.Join(appDir, "release.yaml")
			err = ioutil.WriteFile(releaseYamlOutPath, bytes, 0755)
			if err != nil {
				return errors.Wrapf(err, "write file %s", releaseYamlOutPath)
			}
			if verbose {
				log.Logger().Infof("Read release notes URL %s and git repo url %s from release.yaml\nWriting release."+
					"yaml from chartName to %s", releaseNotesURL, gitRepo, releaseYamlOutPath)
			}
		} else if os.IsNotExist(err) {
			if verbose {

				log.Logger().Infof("Not adding release.yaml as not present in chart. Only files in %s are:",
					templatesDir)
				err := files.ListDirectory(templatesDir, true)
				if err != nil {
					return err
				}
			}
		} else {
			return errors.Wrapf(err, "reading release.yaml from %s", templatesDir)
		}
	}
	chartYamlPath := filepath.Join(requirementDir, helm.ChartFileName)
	if _, err := os.Stat(chartYamlPath); err == nil {
		bytes, err := ioutil.ReadFile(chartYamlPath)
		if err != nil {
			return errors.Wrapf(err, "read %s from %s", helm.ChartFileName, requirementDir)
		}
		chart := helmchart.Metadata{}
		err = yaml.Unmarshal(bytes, &chart)
		if err != nil {
			return errors.Wrapf(err, "unmarshal %s", chartYamlPath)
		}
		description = chart.Description
	} else if os.IsNotExist(err) {
		if verbose {
			log.Logger().Infof("Not adding %s as not present in chart. Only files in %s are:", helm.ChartFileName,
				requirementDir)
			err := files.ListDirectory(requirementDir, true)
			if err != nil {
				return err
			}
		}
	} else {
		return errors.Wrapf(err, "stat Chart.yaml from %s", requirementDir)
	}
	// Need to copy over any referenced files, and their schemas
	rootValues, err := helm.LoadValuesFile(rootValuesFileName)
	if err != nil {
		return err
	}
	schemas := make(map[string][]string)
	possibles := make(map[string]string)
	if _, err := os.Stat(requirementDir); err == nil {
		fileSlice, err := ioutil.ReadDir(requirementDir)
		if err != nil {
			return errors.Wrapf(err, "unable to list files in %s", requirementDir)
		}
		possibleReadmes := make([]string, 0)
		for _, file := range fileSlice {
			fileName := strings.ToUpper(file.Name())
			if fileName == "README.MD" || fileName == "README" {
				possibleReadmes = append(possibleReadmes, filepath.Join(requirementDir, file.Name()))
			}
		}
		if len(possibleReadmes) > 1 {
			if verbose {
				log.Logger().Warnf("Unable to add README to PR for %s as more than one exists and not sure which to"+
					" use %s", requirementName, possibleReadmes)
			}
		} else if len(possibleReadmes) == 1 {
			bytes, err := ioutil.ReadFile(possibleReadmes[0])
			if err != nil {
				return errors.Wrapf(err, "unable to read file %s", possibleReadmes[0])
			}
			appReadme = string(bytes)
		}

		for _, f := range fileSlice {
			ignore, err := files.IgnoreFile(f.Name(), helm.DefaultValuesTreeIgnores)
			if err != nil {
				return err
			}
			if !f.IsDir() && !ignore {
				key := f.Name()
				// Handle .schema. files specially
				if parts := strings.Split(key, ".schema."); len(parts) > 1 {
					// this is a file named *.schema.*, the part before .schema is the key
					if _, ok := schemas[parts[0]]; !ok {
						schemas[parts[0]] = make([]string, 0)
					}
					schemas[parts[0]] = append(schemas[parts[0]], filepath.Join(requirementDir, f.Name()))
				}
				possibles[key] = filepath.Join(requirementDir, f.Name())

			}
		}
	} else if !os.IsNotExist(err) {
		return errors.Wrap(err, fmt.Sprintf("error reading %s", requirementDir))
	}
	if verbose && appReadme == "" {
		log.Logger().Infof("Not adding App Readme as no README, README.md, readme or readme.md found in %s", requirementDir)
	}
	app, filename, err := LocateAppResource(helmer, requirementDir, requirementName)
	if err != nil {
		return errors.WithStack(err)
	}
	err = EnhanceChartWithAppMetadata(requirementDir, app, requirementRepository, appDir, filename)
	if err != nil {
		return errors.WithStack(err)
	}
	readme := helm.GenerateReadmeForChart(requirementName, requirementVersion, description, requirementRepository, gitRepo, releaseNotesURL, appReadme)
	readmeOutPath := filepath.Join(appDir, "README.MD")
	err = ioutil.WriteFile(readmeOutPath, []byte(readme), 0755)
	if err != nil {
		return errors.Wrapf(err, "write README.md to %s", appDir)

		if verbose {
			log.Logger().Infof("Writing README.md to %s", readmeOutPath)
		}
		externalFileHandler := func(path string, element map[string]interface{}, key string) error {
			fileName, _ := filepath.Split(path)
			err := files.CopyFile(path, filepath.Join(appDir, fileName))
			if err != nil {
				return errors.Wrapf(err, "copy %s to %s", path, appDir)
			}
			// key for schema is the filename without the extension
			schemaKey := strings.TrimSuffix(fileName, filepath.Ext(fileName))
			if schemaPaths, ok := schemas[schemaKey]; ok {
				for _, schemaPath := range schemaPaths {
					fileName, _ := filepath.Split(schemaPath)
					schemaOutPath := filepath.Join(appDir, fileName)
					err := files.CopyFile(schemaPath, schemaOutPath)
					if err != nil {
						return errors.Wrapf(err, "copy %s to %s", schemaPath, appDir)
					}
					if verbose {
						log.Logger().Infof("Writing %s to %s", fileName, schemaOutPath)
					}
				}
			}
			return nil
		}
		err = helm.HandleExternalFileRefs(rootValues, possibles, "", externalFileHandler)
		if err != nil {
			return err
		}
	}
	return nil
}

// EnhanceChartWithAppMetadata will update the app in chartDir with app metadata,
// writing the custom resource to the outputDir as a new file called filename
func EnhanceChartWithAppMetadata(chartDir string, app *jenkinsv1.App, repository string, outputDir string,
	filename string) error {
	outputTemplateDir := filepath.Join(outputDir, "templates")
	templatesDirExists, err := files.DirExists(outputTemplateDir)
	if err != nil {
		return err
	}
	if !templatesDirExists {
		os.Mkdir(outputTemplateDir, os.ModePerm)
	}
	outputFilename := filepath.Join(outputTemplateDir, filename)
	err = AddAppMetaData(chartDir, app, repository)
	if err != nil {
		return errors.Wrapf(err, "enhancing %s with app metadata", app.Name)
	}
	err = helm.SaveFile(outputFilename, app)
	if err != nil {
		return errors.Wrapf(err, "saving enhanced app metadata to %s for app %s", outputFilename, app.Name)
	}
	return nil
}

// AddAppMetaData applies chart metadata to an App resource
func AddAppMetaData(chartDir string, app *jenkinsv1.App, repository string) error {
	metadata, err := helm.LoadChartFile(filepath.Join(chartDir, "Chart.yaml"))
	if err != nil {
		return errors.Wrapf(err, "error loading chart from %s", chartDir)
	}
	if app.Annotations == nil {
		app.Annotations = make(map[string]string)
	}
	app.Annotations[helm.AnnotationAppDescription] = metadata.GetDescription()
	if _, err = url.Parse(repository); err != nil {
		return errors.Wrap(err, "Invalid repository url")
	}
	app.Annotations[helm.AnnotationAppRepository] = stringhelpers.SanitizeURL(repository)
	if app.Labels == nil {
		app.Labels = make(map[string]string)
	}
	app.Labels[helm.LabelAppName] = metadata.Name
	app.Labels[helm.LabelAppVersion] = metadata.Version
	return nil
}

// LocateAppResource finds or creates a resource of Kind: App in a given appName rooted in chartDir,
// writing it to outputDir. The template with the
func LocateAppResource(helmer helm.Helmer, chartDir string, appName string) (*jenkinsv1.App,
	string, error) {

	templateWorkDir := filepath.Join(chartDir, "output")
	templateWorkDirExists, err := files.DirExists(templateWorkDir)
	if err != nil {
		return nil, "", err
	}
	if !templateWorkDirExists {
		err = os.Mkdir(templateWorkDir, os.ModePerm)
		if err != nil {
			return nil, "", errors.Wrapf(err, "creating template work dir %s", templateWorkDir)
		}
	}
	defaultApp := &jenkinsv1.App{
		TypeMeta: metav1.TypeMeta{
			Kind:       "App",
			APIVersion: jenkinsio.GroupName + "/" + jenkinsio.Version,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: appName,
		},
		Spec: jenkinsv1.AppSpec{},
	}
	err = helmer.Template(chartDir, appName, "", templateWorkDir, false, make([]string, 0), make([]string, 0), make([]string, 0))
	if err != nil {
		templateWorkDir = chartDir
	}
	completedTemplatesDir := filepath.Join(templateWorkDir, appName, "templates")
	templates, _ := ioutil.ReadDir(completedTemplatesDir)

	filename := "app.yaml"
	possibles := make([]string, 0)
	app := &jenkinsv1.App{}
	for _, template := range templates {
		appBytes, err := ioutil.ReadFile(filepath.Join(completedTemplatesDir, template.Name()))
		if err != nil {
			return nil, "", errors.Wrapf(err, "reading file %s", filename)
		}
		err = yaml.Unmarshal(appBytes, app)
		if err == nil {
			if app.Kind == "App" {
				// Use the first located resource
				filename = template.Name()
				possibles = append(possibles, app.Name)
			}
		}
	}

	switch size := len(possibles); {
	case size > 1:
		return nil, "", errors.Errorf("at most one resource of Kind: App can be specified but found %v", possibles)
	case size == 0:
		//If we are adding a generated app, we need the placeholder to be the App object, otherwise a random one
		//from templates is going to be used instead
		app = defaultApp
	}

	return app, filename, nil
}
