package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sigstore/cosign/cmd/cosign/cli/generate"
	"github.com/sigstore/k8s-manifest-sigstore/pkg/k8smanifest"
	k8smnfutil "github.com/sigstore/k8s-manifest-sigstore/pkg/util"
	kubeutil "github.com/sigstore/k8s-manifest-sigstore/pkg/util/kubeutil"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

const letterBytes = "1234567890abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
const filenameIfInputIsDir = "manifest.yaml"

type FlagsInput struct {
	chartsPath                 string
	keyPath                    string
	owaspDependencyCheckScan   string
	owaspDependencyCheckOutput string
	kubesecScan                bool
	kubesecOutput              string
}

// goCmd represents the go command
var goCmd = &cobra.Command{
	Use:   "go",
	Short: "gather and upload artifacts in one go",
	Long: `EXAMPLE gathernupload go [FLAGS]
	Flags
	-c, --charts-directory path/to/charts/directory
	-b, --backend-storage-key path/to/key
	--owasp-dependency-check-scan path/to/project
	--owasp-dependency-check-output path/to/dependency/check/output.json
	--kubesec-scan true/false , true=scan-manifest, false=skip-scanning
	--kubesec-output path/to/kubesec/output.json

	(script can't take any args)
	`,
	Run: func(cmd *cobra.Command, args []string) {

		if len(args) > 0 {
			cmd.Help()
			os.Exit(1)
		}

		chartsPath, _ := cmd.Flags().GetString("charts-directory")
		backendStorageKey, _ := cmd.Flags().GetString("backend-storage-key")
		owaspDependencyCheckScan, _ := cmd.Flags().GetString("owasp-dependency-check-scan")
		owaspDependencyCheckOutput, _ := cmd.Flags().GetString("owasp-dependency-check-output")
		kubesecScan, _ := cmd.Flags().GetBool("kubesec-scan")
		kubesecOutput, _ := cmd.Flags().GetString("kubesec-output")

		flags := FlagsInput{chartsPath, backendStorageKey, owaspDependencyCheckScan, owaspDependencyCheckOutput, kubesecScan, kubesecOutput}

		fmt.Printf("%s %s %s %s %t %s\n", chartsPath, backendStorageKey, owaspDependencyCheckScan, owaspDependencyCheckOutput, kubesecScan, kubesecOutput)

		// input := Input(cmd.Flags().GetString("charts-directory"))
		id := randStringBytes(15)

		err := makeKeyPair(cmd.Context())
		if err != nil {
			log.Errorf("creating key: %w", err)
			os.Exit(1)
		}

		err = gatherArtifacts(id, flags)
		if err != nil {
			log.Errorf("gathering artifacts: %w", err)
			os.Exit(1)
		}

	},
}

func gatherArtifacts(id string, flags FlagsInput) error {
	//create folder
	dirname := id + "_artifacts"

	if _, err := os.Stat(flags.chartsPath); os.IsNotExist(err) {
		log.Errorf("%s dir not found", flags.chartsPath)
		return err
	}

	err := os.Mkdir(dirname, 0755)
	if err != nil {
		log.Errorf("making dir %s failed\n", dirname)
		return err
	}

	//render manifest from charts
	prep_command := "helm template " + flags.chartsPath + " --output-dir " + dirname
	render_cmd := exec.Command("bash", "-c", prep_command)

	err = render_cmd.Run()
	if err != nil {
		log.Errorf("rendering charts failed\n")
		return err
	}

	inside := filepath.Join(dirname, "/Charts/templates")
	files, err := ioutil.ReadDir(inside)
	if err != nil {
		log.Errorf("reading rendered charts failed\n")
		return err
	}

	var imageRef string = ""
	var keyPath string = "cosign.key"
	var applySignatureConfigMap bool = false
	var updateAnnotation bool = true
	var imageAnnotations []string

	//sign kubernetes manifest
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		temp_full_path := filepath.Join(inside, file.Name())

		err = giveManifestId(temp_full_path, file.Name(), id)
		if err != nil {
			log.Errorf("gatherArtifacts - attaching id to manifest %s failed\n", file.Name())
			return err
		}

		err = sign(temp_full_path, imageRef, keyPath, temp_full_path, applySignatureConfigMap, updateAnnotation, imageAnnotations)
		if err != nil {
			log.Errorf("gatherArtifacts - signing manifest %s failed\n", file.Name())
			return err
		}
	}

	//Check OWASP Dependency Check Output
	if _, err := os.Stat(flags.owaspDependencyCheckOutput); errors.Is(err, os.ErrNotExist) {
		log.Errorf("gatherArtifacts - OWASP Dependency Check output not found")
		os.Exit(1)
	} else {
		err = copyFile(flags.owaspDependencyCheckOutput, dirname+"/dependency-check-report.json")
		if err != nil {
			return err
		}
	}

	//Check Kubesec Output
	if _, err := os.Stat(flags.kubesecOutput); errors.Is(err, os.ErrNotExist) {
		log.Errorf("gatherArtifacts - kubesec output not found")
		os.Exit(1)
	} else {
		err = copyFile(flags.kubesecOutput, dirname+"/kubesec-output.json")
		if err != nil {
			return err
		}
	}

	return nil
}
func copyFile(src string, dst string) error {
	fin, err := os.Open(src)
	if err != nil {
		return err
	}
	defer fin.Close()

	fout, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer fout.Close()

	_, err = io.Copy(fout, fin)

	if err != nil {
		return err
	}

	return nil
}

func giveManifestId(temp_full_path string, filename string, id string) error {
	files, err := ioutil.ReadFile(temp_full_path)
	if err != nil {
		log.Errorf("giveManifestId - error reading files %s", temp_full_path)
		return err
	}

	var manifest map[string]interface{}
	if err := yaml.Unmarshal(files, &manifest); err != nil {
		log.Errorf("giveManifestId - unmarshal yaml failed")
		return err
	}
	metadata := manifest["metadata"].(map[string]interface{})
	annotationsExist := keyExist(manifest["metadata"].(map[string]interface{}), "annotations")

	if !annotationsExist {
		metadata["annotations"] = map[string]interface{}{}
		manifest["metadata"] = metadata
	}

	annotations := manifest["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})
	annotations["gnup-id"] = id + "_" + filename
	manifest["metadata"].(map[string]interface{})["annotations"] = annotations

	newYaml, err := yaml.Marshal(manifest)
	if err != nil {
		log.Errorf("giveManifestId - marshaling to newYaml failed")
		return err
	}

	err = ioutil.WriteFile(temp_full_path, newYaml, 0)
	if err != nil {
		log.Errorf("giceManifestId - writefile to %s failed", temp_full_path)
		return err
	}

	return nil
}

func keyExist(data map[string]interface{}, key string) bool {
	if _, ok := data[key]; ok {
		return true
	}
	return false
}

func randStringBytes(n int) string {
	rand.Seed(time.Now().UnixNano())
	b := make([]byte, n)
	for i := range b {
		b[i] = letterBytes[rand.Intn(len(letterBytes))]
	}
	return string(b)
}

func makeKeyPair(ctx context.Context) error {
	var empty_list []string

	_, passphrase := os.LookupEnv("COSIGN_PASSWORD")
	if !passphrase {
		os.Setenv("COSIGN_PASSWORD", "")
	}

	err := generate.GenerateKeyPairCmd(ctx, "", empty_list)
	if err != nil {
		return err
	}

	return nil
}

func sign(inputDir, imageRef, keyPath, output string, applySignatureConfigMap, updateAnnotation bool, annotations []string) error {
	if output == "" && updateAnnotation {
		if isDir, _ := k8smnfutil.IsDir(inputDir); isDir {
			// e.g.) "./yamls/" --> "./yamls/manifest.yaml.signed"
			output = filepath.Join(inputDir, filenameIfInputIsDir+".signed")
		} else {
			// e.g.) "configmap.yaml" --> "configmap.yaml.signed"
			output = inputDir + ".signed"
		}
	}

	var anntns map[string]interface{}
	var err error
	if len(annotations) > 0 {
		anntns, err = parseAnnotations(annotations)
		if err != nil {
			return err
		}
	}

	so := &k8smanifest.SignOption{
		ImageRef:         imageRef,
		KeyPath:          keyPath,
		Output:           output,
		UpdateAnnotation: updateAnnotation,
		ImageAnnotations: anntns,
	}

	if applySignatureConfigMap && strings.HasPrefix(output, kubeutil.InClusterObjectPrefix) {
		so.ApplySigConfigMap = true
	}

	_, err = k8smanifest.Sign(inputDir, so)
	if err != nil {
		return err
	}
	if so.UpdateAnnotation {
		finalOutput := output
		if strings.HasPrefix(output, kubeutil.InClusterObjectPrefix) && !applySignatureConfigMap {
			finalOutput = k8smanifest.K8sResourceRef2FileName(output)
		}
		log.Info("signed manifest generated at ", finalOutput)
	}
	return nil
}

func parseAnnotations(annotations []string) (map[string]interface{}, error) {
	annotationsMap := map[string]interface{}{}

	for _, annotation := range annotations {
		kvp := strings.SplitN(annotation, "=", 2)
		if len(kvp) != 2 {
			return nil, fmt.Errorf("invalid flag: %s, expected key=value", annotation)
		}

		annotationsMap[kvp[0]] = kvp[1]
	}
	return annotationsMap, nil
}

func init() {
	rootCmd.AddCommand(goCmd)

	goCmd.Flags().StringP("charts-directory", "c", "./charts", "path to charts folder/directory")
	goCmd.Flags().StringP("backend-storage-key", "b", "./credentials.json", "path to backend credentials")
	goCmd.Flags().StringP("owasp-dependency-check-scan", "", "", "project's path to scan (leave empty to skip scanning)")
	goCmd.Flags().StringP("owasp-dependency-check-output", "", "./dependency-check-report.json", "path to owasp dependency check output")
	goCmd.Flags().BoolP("kubesec-scan", "", false, "if true, script will scan manifests, else will skip scanning")
	goCmd.Flags().StringP("kubesec-output", "", "./kubesec-output.json", "path to kubesec output")
	goCmd.Flags().BoolP("rm-key", "", true, "delete key after process (both key pair need to be in the same directory)")
}