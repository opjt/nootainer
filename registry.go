//go:build linux

package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

const (
	authURL     = "https://auth.docker.io/token"
	registryURL = "https://registry-1.docker.io"
)

type tokenResponse struct {
	Token string `json:"token"`
}

func getAuthToken(image string) (string, error) {
	url := fmt.Sprintf("%s?service=registry.docker.io&scope=repository:%s:pull", authURL, image)

	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token request returned status %d", resp.StatusCode)
	}

	var token tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return "", fmt.Errorf("token decode failed: %w", err)
	}

	return token.Token, nil
}

type manifestIndex struct {
	Manifests []platformManifest `json:"manifests"`
}

type platformManifest struct {
	Digest   string   `json:"digest"`
	Platform platform `json:"platform"`
}

type platform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
}

type manifest struct {
	Layers []layer `json:"layers"`
}

type layer struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

func registryGet(url, token string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json")
	return http.DefaultClient.Do(req)
}

func getManifest(repoName, tag, token string) (*manifest, error) {
	// 1단계: manifest index 가져오기
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", registryURL, repoName, tag)
	resp, err := registryGet(url, token)
	if err != nil {
		return nil, fmt.Errorf("manifest index request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest index returned status %d", resp.StatusCode)
	}

	var index manifestIndex
	if err := json.NewDecoder(resp.Body).Decode(&index); err != nil {
		return nil, fmt.Errorf("manifest index decode failed: %w", err)
	}

	// 2단계: linux/amd64 manifest digest 찾기
	var digest string
	for _, m := range index.Manifests {
		if m.Platform.Architecture == "amd64" && m.Platform.OS == "linux" {
			digest = m.Digest
			break
		}
	}
	if digest == "" {
		return nil, fmt.Errorf("no linux/amd64 manifest found")
	}

	// 3단계: 실제 manifest 가져오기
	url = fmt.Sprintf("%s/v2/%s/manifests/%s", registryURL, repoName, digest)
	resp2, err := registryGet(url, token)
	if err != nil {
		return nil, fmt.Errorf("manifest request failed: %w", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest returned status %d", resp2.StatusCode)
	}

	var m manifest
	if err := json.NewDecoder(resp2.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest decode failed: %w", err)
	}

	return &m, nil
}

func downloadAndExtractLayer(repoName, digest, token, destDir string) error {
	url := fmt.Sprintf("%s/v2/%s/blobs/%s", registryURL, repoName, digest)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("blob request create failed: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("blob download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("blob download returned status %d", resp.StatusCode)
	}

	gr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("gzip reader failed: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read failed: %w", err)
		}

		target := filepath.Join(destDir, hdr.Name)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			os.Remove(target)
			if err := os.Link(filepath.Join(destDir, hdr.Linkname), target); err != nil {
				return err
			}
		}
	}

	return nil
}

func pull(image, tag string) {
	repoName := "library/" + image

	token, err := getAuthToken(repoName)
	if err != nil {
		log.Fatal("auth failed:", err)
	}
	fmt.Println("token acquired")

	m, err := getManifest(repoName, tag, token)
	if err != nil {
		log.Fatal("manifest failed:", err)
	}
	fmt.Printf("found %d layer(s)\n", len(m.Layers))

	destDir := filepath.Join("rootfs_" + image)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		log.Fatal("mkdir failed:", err)
	}

	for i, l := range m.Layers {
		fmt.Printf("downloading layer %d/%d: %s\n", i+1, len(m.Layers), l.Digest[:25]+"...")
		if err := downloadAndExtractLayer(repoName, l.Digest, token, destDir); err != nil {
			log.Fatal("layer extract failed:", err)
		}
	}

	fmt.Printf("image extracted to %s/\n", destDir)
}
