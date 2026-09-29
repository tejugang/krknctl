package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var allowedRestoreGVKs = map[schema.GroupVersionKind]struct{}{
	{Group: "krkn.krkn-chaos.dev", Version: "v1alpha1", Kind: "KrknUser"}:                   {},
	{Group: "krkn.krkn-chaos.dev", Version: "v1alpha1", Kind: "KrknUserGroup"}:              {},
	{Group: "krkn.krkn-chaos.dev", Version: "v1alpha1", Kind: "KrknOperatorTarget"}:         {},
	{Group: "krkn.krkn-chaos.dev", Version: "v1alpha1", Kind: "KrknOperatorTargetProvider"}: {},
	{Group: "", Version: "v1", Kind: "Secret"}:                                              {},
	{Group: "", Version: "v1", Kind: "ConfigMap"}:                                           {},
}

var expectedArchiveGVKs = map[string]schema.GroupVersionKind{
	"krknuser.json":                   {Group: "krkn.krkn-chaos.dev", Version: "v1alpha1", Kind: "KrknUser"},
	"krknusergroup.json":              {Group: "krkn.krkn-chaos.dev", Version: "v1alpha1", Kind: "KrknUserGroup"},
	"krknoperatortarget.json":         {Group: "krkn.krkn-chaos.dev", Version: "v1alpha1", Kind: "KrknOperatorTarget"},
	"krknoperatortargetprovider.json": {Group: "krkn.krkn-chaos.dev", Version: "v1alpha1", Kind: "KrknOperatorTargetProvider"},
	"auth-secrets.json":               {Group: "", Version: "v1", Kind: "Secret"},
	"user-auth-secrets.json":          {Group: "", Version: "v1", Kind: "Secret"},
	"target-secrets.json":             {Group: "", Version: "v1", Kind: "Secret"},
	"elasticsearch-secrets.json":      {Group: "", Version: "v1", Kind: "Secret"},
	"registry-secrets.json":           {Group: "", Version: "v1", Kind: "Secret"},
	"provider-configmaps.json":        {Group: "", Version: "v1", Kind: "ConfigMap"},
}

const (
	maxExtractFileSize     = 256 << 20
	maxCumulativeExtracted = 1 << 30
)

// RestoreConfig controls restoration of an operator configuration archive.
type RestoreConfig struct {
	// Namespace is the destination namespace for restored resources.
	Namespace string
	// BackupPath is the .tar.gz archive to restore.
	BackupPath string
	// ArchivePath is retained as a compatibility alias for BackupPath.
	// Deprecated: use BackupPath.
	ArchivePath string
}

// RestoreBackup applies an operator admin-settings archive directly through
// the Kubernetes API. The operator process itself does not need to be running.
func RestoreBackup(ctx context.Context, dynClient dynamic.Interface, config RestoreConfig) error {
	if strings.TrimSpace(config.Namespace) == "" {
		return fmt.Errorf("namespace must not be empty")
	}
	archivePath := config.BackupPath
	if archivePath == "" {
		archivePath = config.ArchivePath
	}
	if strings.TrimSpace(archivePath) == "" {
		return fmt.Errorf("backup path must not be empty")
	}
	namespace := config.Namespace
	extractDir, err := os.MkdirTemp("", "krkn-restore-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary directory: %w", err)
	}
	defer os.RemoveAll(extractDir)

	if err := extractTarGz(archivePath, extractDir); err != nil {
		return fmt.Errorf("failed to extract archive: %w", err)
	}
	backupDir, err := findBackupDir(extractDir)
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return fmt.Errorf("failed to read backup directory: %w", err)
	}

	var jsonFiles []string
	var validationFailures []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		filePath := filepath.Join(backupDir, entry.Name())
		objects, err := readListFile(filePath)
		if err != nil {
			return fmt.Errorf("%s: %w", entry.Name(), err)
		}
		for _, object := range objects {
			if err := validateRestoreObjectForFile(entry.Name(), object); err != nil {
				validationFailures = append(validationFailures, formatRestoreFailure(entry.Name(), object, err))
			}
		}
		jsonFiles = append(jsonFiles, filePath)
	}

	if len(validationFailures) > 0 {
		return fmt.Errorf("archive validation failed: %s", strings.Join(validationFailures, "; "))
	}
	if len(jsonFiles) == 0 {
		return fmt.Errorf("no resources were restored")
	}

	var restoreFailures []string
	applied := 0
	for _, filePath := range jsonFiles {
		objects, err := readListFile(filePath)
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(filePath), err)
		}
		for _, object := range objects {
			object.SetNamespace(namespace)
			if err := applyResource(ctx, dynClient, namespace, &object); err != nil {
				restoreFailures = append(restoreFailures, formatRestoreFailure(filepath.Base(filePath), object, err))
				continue
			}
			applied++
		}
	}
	if applied == 0 && len(restoreFailures) == 0 {
		return fmt.Errorf("no resources were restored")
	}
	if len(restoreFailures) > 0 {
		return fmt.Errorf("restore completed with %d resource failures out of %d total: %s", len(restoreFailures), applied+len(restoreFailures), strings.Join(restoreFailures, "; "))
	}
	return nil
}

// Restore is retained for compatibility with the original krknctl API.
// Deprecated: use RestoreBackup with RestoreConfig.
func Restore(ctx context.Context, dynClient dynamic.Interface, archivePath, namespace string) error {
	return RestoreBackup(ctx, dynClient, RestoreConfig{BackupPath: archivePath, Namespace: namespace})
}

func validateRestoreObject(obj unstructured.Unstructured) error {
	gvk := obj.GroupVersionKind()
	if _, ok := allowedRestoreGVKs[gvk]; !ok {
		return fmt.Errorf("resource type %s is not allowed", gvk.String())
	}
	if obj.GetName() == "" {
		return fmt.Errorf("resource is missing metadata.name")
	}
	if obj.IsList() {
		return fmt.Errorf("nested list resource is not allowed")
	}
	if obj.GetKind() == "Secret" {
		if obj.GetName() == "krkn-operator-jwt" {
			return fmt.Errorf("JWT signing Secret cannot be restored")
		}
		labels := obj.GetLabels()
		if labels == nil || (labels["krkn-target-uuid"] == "" &&
			labels["app.kubernetes.io/component"] != "authentication" &&
			labels["app.kubernetes.io/component"] != "user-auth" &&
			labels["app.kubernetes.io/component"] != "elasticsearch-config" &&
			labels["app.kubernetes.io/component"] != "registry") {
			return fmt.Errorf("Secret %q is not operator-managed", obj.GetName())
		}
	}
	if obj.GetKind() == "ConfigMap" {
		labels := obj.GetLabels()
		if labels == nil || labels["krkn.krkn-chaos.dev/provider-config"] != "true" {
			return fmt.Errorf("ConfigMap %q is not an operator provider configuration", obj.GetName())
		}
	}
	return nil
}

func validateRestoreObjectForFile(filename string, obj unstructured.Unstructured) error {
	expected, ok := expectedArchiveGVKs[filename]
	if !ok {
		return fmt.Errorf("archive file %q is not supported", filename)
	}
	if obj.GroupVersionKind() != expected {
		return fmt.Errorf("resource type %s does not match archive file %q", obj.GroupVersionKind().String(), filename)
	}
	return validateRestoreObject(obj)
}

func applyResource(ctx context.Context, dynClient dynamic.Interface, namespace string, obj *unstructured.Unstructured) error {
	gvk := obj.GroupVersionKind()
	gvr := schema.GroupVersionResource{Group: gvk.Group, Version: gvk.Version, Resource: resourceNameForKind(gvk.Kind)}
	resource := dynClient.Resource(gvr).Namespace(namespace)
	// Status is destination-observed state and must never be copied from an
	// archive, including a manually constructed archive.
	delete(obj.Object, "status")

	existing, getErr := resource.Get(ctx, obj.GetName(), metav1.GetOptions{})
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		return fmt.Errorf("failed to check existing resource %s/%s: %w", gvr.Resource, obj.GetName(), getErr)
	}
	if apierrors.IsNotFound(getErr) {
		if _, err := resource.Create(ctx, obj, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("failed to create %s/%s: %w", gvr.Resource, obj.GetName(), err)
		}
	} else {
		obj.SetResourceVersion(existing.GetResourceVersion())
		obj.SetUID(existing.GetUID())
		if _, err := resource.Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("failed to update %s/%s: %w", gvr.Resource, obj.GetName(), err)
		}
	}

	return nil
}

func formatRestoreFailure(filename string, obj unstructured.Unstructured, err error) string {
	kind := obj.GetKind()
	if kind == "" {
		kind = "resource"
	}
	name := obj.GetName()
	if name == "" {
		name = "<unnamed>"
	}
	return fmt.Sprintf("%s %s/%s: %v", filename, kind, name, err)
}

func resourceNameForKind(kind string) string {
	switch kind {
	case "KrknUser":
		return "krknusers"
	case "KrknUserGroup":
		return "krknusergroups"
	case "KrknOperatorTarget":
		return "krknoperatortargets"
	case "KrknOperatorTargetProvider":
		return "krknoperatortargetproviders"
	case "Secret":
		return "secrets"
	case "ConfigMap":
		return "configmaps"
	default:
		return strings.ToLower(kind) + "s"
	}
}

func gvrFromObject(obj unstructured.Unstructured) (schema.GroupVersionResource, error) {
	apiVersion := obj.GetAPIVersion()
	if apiVersion == "" || obj.GetKind() == "" {
		return schema.GroupVersionResource{}, fmt.Errorf("missing apiVersion or kind")
	}
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return schema.GroupVersionResource{}, fmt.Errorf("invalid apiVersion %q: %w", apiVersion, err)
	}
	return schema.GroupVersionResource{Group: gv.Group, Version: gv.Version, Resource: resourceNameForKind(obj.GetKind())}, nil
}

func readListFile(filePath string) ([]unstructured.Unstructured, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", filepath.Base(filePath), err)
	}
	var wrapper struct {
		APIVersion string                   `json:"apiVersion"`
		Kind       string                   `json:"kind"`
		Items      []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", filepath.Base(filePath), err)
	}
	if wrapper.Kind != "List" || wrapper.APIVersion == "" {
		return nil, fmt.Errorf("%s must be a Kubernetes List with apiVersion", filepath.Base(filePath))
	}
	items := make([]unstructured.Unstructured, 0, len(wrapper.Items))
	for _, item := range wrapper.Items {
		items = append(items, unstructured.Unstructured{Object: item})
	}
	return items, nil
}

func extractTarGz(archivePath, destDir string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)
	cumulative, files := int64(0), 0
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("error reading tar: %w", err)
		}
		relPath := filepath.Clean(filepath.FromSlash(header.Name))
		if header.Name == "" || filepath.IsAbs(relPath) || relPath == "." || relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
			return fmt.Errorf("invalid path in archive (path traversal detected): %s", header.Name)
		}
		target := filepath.Join(destDir, relPath)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
			if err != nil {
				return err
			}
			written, copyErr := io.Copy(output, io.LimitReader(tarReader, maxExtractFileSize+1))
			closeErr := output.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if written > maxExtractFileSize {
				return fmt.Errorf("file %s exceeds maximum allowed size", filepath.Base(relPath))
			}
			cumulative += written
			files++
			if cumulative > maxCumulativeExtracted {
				return fmt.Errorf("archive exceeds maximum extracted size (1 GB), files=%d", files)
			}
		}
	}
}

func findBackupDir(tempDir string) (string, error) {
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return "", fmt.Errorf("failed to read extracted archive: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if entry.Name() == backupDirName {
			return filepath.Join(tempDir, entry.Name()), nil
		}
		if strings.HasPrefix(entry.Name(), "krkn-backup-tmp-") {
			candidate := filepath.Join(tempDir, entry.Name(), backupDirName)
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("backup directory not found in archive")
}
