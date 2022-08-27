package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/gorilla/mux"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

type contextKey string

// Singletons
var clientset *kubernetes.Clientset
var dynamicInterface dynamic.Interface

/*
Returns a list of names of namespaces that should be created from a list of students
*/
func getNamespaceNames(students []Student, labName string, isIndividual bool) []string {
	var namespaces []string

	if isIndividual {
		for _, student := range students {
			// Convert "First Last" to first-last to ns-labname-first-last
			name := strings.ToLower(strings.Join(strings.Split(student.name, " "), "-"))
			namespaces = append(namespaces, fmt.Sprintf("ns-%s-%s", labName, name))
		}

		return namespaces
	}

	// Keep track of the groups that already have a namespace
	visited := make(map[int]bool)

	for _, student := range students {
		if student.group != -1 && !visited[student.group] {
			// Convert groupNumber to ns-labname-group-#
			namespaces = append(namespaces, fmt.Sprintf("ns-%s-group-%d", labName, student.group))
			visited[student.group] = true
		}
	}

	return namespaces
}

/*
Checks if file in form with name filename is one of the supported types.
Returns file if supported.
*/
func getFormFile(r *http.Request, filename string, contentTypes ...string) (io.ReadCloser, *Error) {
	file, fileHeader, err := r.FormFile(filename)
	if err != nil {
		return nil, &Error{status: http.StatusBadRequest, message: "Something went wrong while reading file " + filename}
	}

	// Check if the form file matches one of the allowed types
	contentTypeMatch := false
	for _, contentType := range contentTypes {
		if fileHeader.Header["Content-Type"][0] == contentType {
			contentTypeMatch = true
			break
		}
	}
	if !contentTypeMatch {
		// Map list of supported contentTypes to a string
		contentTypesStr := contentTypes[0]
		for i := 1; i < len(contentTypes); i++ {
			contentTypesStr = contentTypesStr + ", " + contentTypes[i]
		}

		return nil, &Error{status: http.StatusUnsupportedMediaType, message: filename + " must be one of " + contentTypesStr + " types"}
	}

	return file, nil
}

/*
Converts students.csv file to a list of students in HTTP context
*/
func studentsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// students contains a csv file
		studentsFile, err := getFormFile(r, "students", "text/csv")

		if err != nil {
			http.Error(w, err.message, err.status)
			return
		}

		students := getStudentsFromCsv(studentsFile)

		ctx := r.Context()
		ctx = context.WithValue(ctx, contextKey("students"), students)
		r = r.WithContext(ctx)

		next.ServeHTTP(w, r)
	})
}

/*
Creates lab environments for students.
HTTP Parameters:
 students: <CSV-file>
 isIndividual: <bool> 	(optional, default true)
 labName: <string>
 deploymentMode: <string> (["YAML", "CHART", "CHART_URL"])
 configuration: <YAML-file>, <TAR-file> OR <string>
*/
func createLabEnvironment(w http.ResponseWriter, r *http.Request) {

	// Get students from HTTP context
	students := r.Context().Value(contextKey("students")).([]Student)

	// Parse parameters
	r.ParseForm()
	labName := strings.ReplaceAll(r.Form.Get("labName"), "-", "") // Remove - from labname
	deploymentMode := r.Form.Get("deploymentMode")
	isIndividual := r.Form.Get("isIndividual") != "false" // default value true

	namespaces := getNamespaceNames(students, labName, isIndividual)

	// Check if the lab already exists, if it doesn't create the namespace for it and create a read-only role for the lab namespace
	labExists, err := namespaceExists(clientset, "ns-"+labName)
	if err != nil {
		http.Error(w, "Something went wrong while fetching namespaces", http.StatusInternalServerError)
		return
	}

	if !labExists {
		err := createNamespace(clientset, "ns-"+labName)
		if err != nil {
			http.Error(w, "Something went wrong while creating namespace ns-"+labName, http.StatusInternalServerError)
			return
		}

		err = createRole(clientset, "student", "ns-"+labName, []string{"list", "get", "watch"})
		if err != nil {
			http.Error(w, "Something went wrong while creating role for namespace ns-"+labName, http.StatusInternalServerError)
			return
		}
	}

	// List of namespaces that are new (in case of adding groups/students to existing labs)
	// Used to keep track in which namespaces the configuration should be deployed
	var newNamespaces []string

	// Create the namespaces
	for _, namespace := range namespaces {
		// Check if namespace already exists
		namespaceExists, err := namespaceExists(clientset, namespace)
		if err != nil {
			http.Error(w, "Something went wrong while fetching namespaces", http.StatusInternalServerError)
			return
		}

		if namespaceExists {
			continue
		}

		err = createNamespace(clientset, namespace)
		if err != nil {
			http.Error(w, "Something went wrong while creating namespace "+namespace, http.StatusInternalServerError)
			return
		}

		newNamespaces = append(newNamespaces, namespace)
	}

	userConfigs := map[string]string{}

	// Create users and apply RBAC authorization
	for _, namespace := range newNamespaces {
		username := strings.Replace(namespace, "ns-"+labName+"-", "", -1)

		// Create a ServiceAccount for the user
		token, err := createServiceAccount(clientset, username, namespace)
		if err != nil {
			http.Error(w, "Something went wrong while creating service account "+username+" in namespace "+namespace, http.StatusInternalServerError)
			return
		}

		// Create a full-permission Role for the namespace
		if err = createRole(clientset, "student", namespace, []string{"*"}); err != nil {
			http.Error(w, "Something went wrong while creating Role student for namespace "+namespace, http.StatusInternalServerError)
			return
		}

		// Bind the full-permission Role to the ServiceAccount of the user
		if err = createRoleBinding(clientset, "student-binding", namespace, username, namespace, "student"); err != nil {
			http.Error(w, "Something went wrong while creating RoleBinding student-binding for namespace "+namespace+" and user "+username, http.StatusInternalServerError)
			return
		}

		// Bind the read-only Role from the lab namespace to the ServiceAccount of the user
		if err = createRoleBinding(clientset, "student-binding-"+username, "ns-"+labName, username, namespace, "student"); err != nil {
			http.Error(w, "Something went wrong while creating RoleBinding student-binding-"+username+" for namespace ns-"+labName, http.StatusInternalServerError)
			return
		}

		// Bind the read-namespaces-cr to the ServiceAccount of the user
		if err = createReadNamespacesClusterRoleBinding(clientset, labName, username, namespace); err != nil {
			http.Error(w, "Something went wrong while creating ClusterRoleBinding for user "+username, http.StatusInternalServerError)
			return
		}

		// Add the token to the list of tokens
		userConfigs[username] = token
	}

	// Get the manifest in different ways based on deploymentMode
	var manifestFile io.Reader
	switch deploymentMode {
	case "YAML":
		configFile, err := getFormFile(r, "config", "text/yaml")
		if err != nil {
			http.Error(w, err.message, err.status)
			return
		}

		manifestFile = configFile
	case "CHART":
		helmFile, e := getFormFile(r, "config", "application/gzip", "application/octet-stream")
		if e != nil {
			http.Error(w, e.message, e.status)
			return
		}

		chart, err := loader.LoadArchive(helmFile)
		if err != nil {
			http.Error(w, "Something went wrong while parsing the chart", http.StatusInternalServerError)
			return
		}

		kubeYaml, err := convertChartToYaml(chart)
		if err != nil {
			http.Error(w, "Something went wrong while converting chart to YAML", http.StatusInternalServerError)
			return
		}

		manifestFile = strings.NewReader(*kubeYaml)
	case "CHART_URL":
		chartUrl := r.Form.Get("config")

		actionConfig := new(action.Configuration)

		kubeconfigPath := getKubeConfig()
		if err := actionConfig.Init(kube.GetConfig(*kubeconfigPath, "", "default"), "default", os.Getenv("HELM_DRIVER"), nil); err != nil {
			http.Error(w, "Something went wrong while initiating the action configuration", http.StatusInternalServerError)
			return
		}

		settings := cli.New()
		iCli := action.NewInstall(actionConfig)

		chartPath, err := iCli.LocateChart(chartUrl, settings)
		if err != nil {
			http.Error(w, "Something went wrong while locating the chart", http.StatusInternalServerError)
			return
		}

		chart, err := loader.Load(chartPath)
		if err != nil {
			http.Error(w, "Something went wrong while loading the chart", http.StatusInternalServerError)
			return
		}

		kubeYaml, err := convertChartToYaml(chart)
		if err != nil {
			http.Error(w, "Something went wrong while converting chart to YAML", http.StatusInternalServerError)
			return
		}

		manifestFile = strings.NewReader(*kubeYaml)
	}

	// Deploy the manifest on the namespaces
	if err := handleManifest(clientset, dynamicInterface, manifestFile, labName, newNamespaces, labExists); err != nil {
		http.Error(w, "Something went wrong while deploying manifest", http.StatusInternalServerError)
		return
	}

	fmt.Println(newNamespaces)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(userConfigs)
}

func deleteLab(w http.ResponseWriter, r *http.Request) {
	// Get URL parameter
	params := mux.Vars(r)
	labName := strings.ReplaceAll(params["labName"], "-", "") // Remove - from labname

	// Delete all namespaces of which the name starts with ns-labName- or are the general namespace
	namespaces, err := clientset.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		http.Error(w, "Something went wrong while listing the namespaces", http.StatusInternalServerError)
		return
	}

	for _, namespace := range namespaces.Items {
		if namespace.Name == "ns-"+labName || strings.HasPrefix(namespace.Name, "ns-"+labName+"-") {
			if err := clientset.CoreV1().Namespaces().Delete(context.TODO(), namespace.Name, metav1.DeleteOptions{}); err != nil {
				http.Error(w, "Something went wrong while deleting namespace "+namespace.Name, http.StatusInternalServerError)
				return
			}
		}
	}

	// Delete all ClusterRoleBindings of which the name starts with read-namespaces-crb-labName-
	clusterRoleBindings, err := clientset.RbacV1().ClusterRoleBindings().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		http.Error(w, "Something went wrong while listing the ClusterRoleBindings", http.StatusInternalServerError)
		return
	}

	for _, clusterRoleBinding := range clusterRoleBindings.Items {
		if strings.HasPrefix(clusterRoleBinding.Name, "read-namespaces-crb-"+labName+"-") {
			if err := clientset.RbacV1().ClusterRoleBindings().Delete(context.TODO(), clusterRoleBinding.Name, metav1.DeleteOptions{}); err != nil {
				http.Error(w, "Something went wrong while deleting namespace "+clusterRoleBinding.Name, http.StatusInternalServerError)
				return
			}
		}
	}

}

func hello(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "Hello world!")
}

/*
Helper function that creates the read-namespaces-cr if it does not yet exist
*/
func createNamespaceClusterRoleIfNotExists() error {
	readNamespaceClusterRoleExists, err := readNamespaceClusterRoleExists(clientset)
	if err != nil {
		return err
	}
	if !readNamespaceClusterRoleExists {
		if err := createReadNamespacesClusterRole(clientset); err != nil {
			return err
		}
	}

	return nil
}

func main() {
	// Initialise singletons
	cs, dd, err := getClientSet()
	if err != nil {
		panic(err.Error())
	}
	clientset = cs
	dynamicInterface = dd

	if err := createNamespaceClusterRoleIfNotExists(); err != nil {
		panic(err.Error())
	}

	// Set up API
	router := mux.NewRouter()

	router.HandleFunc("/", hello).Methods("GET")
	router.HandleFunc("/lab", studentsMiddleware(createLabEnvironment)).Methods("POST")
	router.HandleFunc("/lab/{labName}", deleteLab).Methods("DELETE")

	http.Handle("/", router)
	fmt.Println("Listening on :3000")
	http.ListenAndServe(":3000", nil)
}
