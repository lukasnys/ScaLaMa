package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// Singleton
var kubeconfig *string

func getKubeConfig() *string {
	if kubeconfig != nil {
		return kubeconfig
	}

	// Initialize singleton
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	flag.Parse()

	return kubeconfig
}

func getClientSet() (*kubernetes.Clientset, dynamic.Interface, error) {
	// Attempts to build config inside cluster, if it fails build outside cluster
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeConfig := getKubeConfig()
		config, err = clientcmd.BuildConfigFromFlags("", *kubeConfig)

		if err != nil {
			return nil, nil, err
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, err
	}

	dynamicInterface, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, nil, err
	}

	return clientset, dynamicInterface, nil
}

func createNamespace(clientSet *kubernetes.Clientset, name string) error {
	nsSpec := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}

	_, err := clientSet.CoreV1().Namespaces().Create(context.TODO(), nsSpec, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	return nil
}

func namespaceExists(clientset *kubernetes.Clientset, name string) (bool, error) {
	namespaces, err := clientset.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return false, err
	}

	for _, namespace := range namespaces.Items {
		if namespace.Name == name {
			return true, nil
		}
	}

	return false, nil
}

func convertChartToYaml(chart *chart.Chart) (*string, error) {
	options := chartutil.ReleaseOptions{
		Name:      "test-name",
		Namespace: "default",
	}

	caps := chartutil.Capabilities{}

	values, err := chartutil.ToRenderValues(chart, chart.Values, options, &caps)
	if err != nil {
		return nil, err
	}

	out, err := engine.Render(chart, values)
	if err != nil {
		return nil, err
	}

	kubeYaml := ""

	for k, v := range out {
		filename := filepath.Base(k)
		if filename == "NOTES.txt" {
			continue
		}

		if v == "\n" {
			continue
		}

		kubeYaml += "---\n# Source: "
		kubeYaml += fmt.Sprintf("%s\n", k)
		kubeYaml += string(v)
	}

	return &kubeYaml, nil
}

func handleManifestHelper(decoder *yamlutil.YAMLOrJSONDecoder) (*unstructured.Unstructured, map[string]interface{}, *meta.RESTMapping, error) {
	var rawObj runtime.RawExtension
	if err := decoder.Decode(&rawObj); err != nil {
		return nil, nil, nil, err
	}

	obj, gvk, err := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme).Decode(rawObj.Raw, nil, nil)
	if err != nil {
		return nil, nil, nil, err
	}

	unstructuredMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, nil, nil, err
	}

	unstructuredObj := &unstructured.Unstructured{Object: unstructuredMap}

	gr, err := restmapper.GetAPIGroupResources(clientset.Discovery())
	if err != nil {
		return nil, nil, nil, err
	}

	mapper := restmapper.NewDiscoveryRESTMapper(gr)
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, nil, nil, err
	}

	return unstructuredObj, unstructuredMap, mapping, nil
}

// Creates objects from YAML manifest in every namespace
func handleManifest(clientset *kubernetes.Clientset, dynamicInterface dynamic.Interface, file io.Reader, labName string, namespaces []string, labExists bool) error {
	var file1 bytes.Buffer

	var decoder *yamlutil.YAMLOrJSONDecoder
	var err error

	// If lab doesn't exist, create the singleInstance stuff
	if !labExists {
		file2 := io.TeeReader(file, &file1)

		decoder = yamlutil.NewYAMLOrJSONDecoder(file2, 100)

		// Loop through manifest and create all singleInstances
		for {
			unstructuredObj, unstructuredMap, mapping, e := handleManifestHelper(decoder)
			err = e
			if err != nil {
				break
			}

			metadata := unstructuredMap["metadata"].(map[string]interface{})
			// Default value is true
			singleInstance := true
			if metadata != nil {
				singleInstance = (metadata["single_instance"] == nil || metadata["single_instance"].(bool))
			}

			if !singleInstance {
				continue
			}

			var dri dynamic.ResourceInterface
			unstructuredObj.SetNamespace("ns-" + labName)
			dri = dynamicInterface.Resource(mapping.Resource).Namespace(unstructuredObj.GetNamespace())

			if _, err := dri.Create(context.Background(), unstructuredObj, metav1.CreateOptions{}); err != nil {
				return err
			}
		}

		if err != io.EOF {
			return err
		}
	}

	if !labExists {
		decoder = yamlutil.NewYAMLOrJSONDecoder(&file1, 100)
	} else {
		decoder = yamlutil.NewYAMLOrJSONDecoder(file, 100)
	}

	// Keep reading objects until EOF
	for {
		unstructuredObj, unstructuredMap, mapping, err := handleManifestHelper(decoder)
		if err != nil {
			break
		}

		metadata := unstructuredMap["metadata"].(map[string]interface{})
		// Default value is true
		singleInstance := true
		if metadata != nil {
			singleInstance = (metadata["single_instance"] == nil || metadata["single_instance"].(bool))
		}

		// Skip the ones we only had to make once
		if singleInstance {
			continue
		}

		// Create objects from manifest in every namespace
		for _, namespace := range namespaces {
			var dri dynamic.ResourceInterface
			unstructuredObj.SetNamespace(namespace)
			dri = dynamicInterface.Resource(mapping.Resource).Namespace(unstructuredObj.GetNamespace())

			if _, err := dri.Create(context.Background(), unstructuredObj, metav1.CreateOptions{}); err != nil {
				return err
			}
		}
	}

	// If error is not EOF, then something is wrong
	if err != io.EOF {
		return err
	}

	return nil
}
