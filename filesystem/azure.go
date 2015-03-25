package filesystem

import (
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"strings"

	"github.com/MSOpenTech/azure-sdk-for-go/storage"
)

type AzureClient struct {
	client     *storage.StorageClient
	blobClient *storage.BlobStorageClient
}

type AzureFile struct {
	path   string
	logger *log.Logger
	client *storage.BlobStorageClient
}

// convertToAzurePath function
// convertToAzurePath splits the given name into two parts
// The first part represents the container's name, and the length of it shoulb be 32 due to Azure restriction
// The second part represents the blob's name
// It will return any error while converting
func convertToAzurePath(name string) (string, string, error) {
	afterSplit := strings.Split(name, "/")
	if len(afterSplit) != 2 {
		return "", "", fmt.Errorf("azureClient : need correct Azure storage path, example : container/blob")
	}
	if len(afterSplit[0]) != 32 {
		return "", "", fmt.Errorf("azureClient : the length of container should be 32")
	}
	return afterSplit[0], afterSplit[1], nil
}

// AzureClient -> Exist function
// Only check the BlobName if exist or not
// User should Provide corresponding ContainerName
func (c *AzureClient) Exists(name string) (bool, error) {
	containerName, blobName, err := convertToAzurePath(name)
	if err != nil {
		return false, err
	}
	return c.blobClient.BlobExists(containerName, blobName)
}

// AzureClient -> Rename function
// Azure prevent user renaming their blob
// Thus this function firstly copy the source blob,
// when finished, delete the source blob.
// http://stackoverflow.com/questions/3734672/azure-storage-blob-rename
func (c *AzureClient) Rename(oldpath, newpath string) error {
	exist, err := c.Exists(oldpath)
	if err != nil {
		return err
	}
	if !exist {
		return fmt.Errorf("azureClient : oldpath does not exist")
	}
	srcContainerName, srcBlobName, err := convertToAzurePath(oldpath)
	if err != nil {
		return err
	}
	dstContainerName, dstBlobName, err := convertToAzurePath(newpath)
	if err != nil {
		return err
	}
	dstBlobUrl := c.blobClient.GetBlobUrl(dstContainerName, dstBlobName)
	srcBlobUrl := c.blobClient.GetBlobUrl(srcContainerName, srcBlobName)
	err = c.blobClient.CopyBlob(dstContainerName, dstBlobName, srcBlobUrl)
	if err != nil {
		return err
	}
	if dstBlobUrl != srcBlobUrl {
		err = c.blobClient.DeleteBlob(srcContainerName, srcBlobName)
		if err != nil {
			return err
		}
	}
	return nil
}

// AzureClient -> OpenReadCloser function
// implement by the providing function
func (c *AzureClient) OpenReadCloser(name string) (io.ReadCloser, error) {
	containerName, blobName, err := convertToAzurePath(name)
	if err != nil {
		return nil, err
	}
	return c.blobClient.GetBlob(containerName, blobName)
}

//AzureClient -> OpenWriteCloser function
// If not exist, Create corresponding Container and blob.
// At present, AzureFile.Write has a capacity restriction(10 * 1024 * 1024 bytes).
// I will implent unlimited version in the future.
func (c *AzureClient) OpenWriteCloser(name string) (io.WriteCloser, error) {
	exist, err := c.Exists(name)
	if err != nil {
		return nil, err
	}
	containerName, blobName, err := convertToAzurePath(name)
	if err != nil {
		return nil, err
	}
	if !exist {
		_, err = c.blobClient.CreateContainerIfNotExists(containerName, storage.ContainerAccessTypeBlob)
		if err != nil {
			return nil, err
		}
		err = c.blobClient.CreateBlockBlob(containerName, blobName)
		if err != nil {
			return nil, err
		}
	}
	return &AzureFile{
		path:   name,
		logger: log.New(os.Stdout, "", log.Lshortfile|log.LstdFlags),
		client: c.blobClient,
	}, nil
}

func (f *AzureFile) Write(b []byte) (int, error) {
	cnt, blob, err := convertToAzurePath(f.path)
	if err != nil {
		return 0, nil
	}
	blockList, err := f.client.GetBlockList(cnt, blob, storage.BlockListTypeAll)
	if err != nil {
		return 0, nil
	}
	blocksLen := len(blockList.CommittedBlocks) + len(blockList.UncommittedBlocks)
	blockId := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%011d\n", blocksLen-1)))
	err = f.client.PutBlock(cnt, blob, blockId, b)
	if err != nil {
		return 0, err
	}
	blockList, err = f.client.GetBlockList(cnt, blob, storage.BlockListTypeAll)
	if err != nil {
		return 0, err
	}
	amendList := []storage.Block{}
	for _, v := range blockList.CommittedBlocks {
		amendList = append(amendList, storage.Block{v.Name, storage.BlockStatusCommitted})
	}
	for _, v := range blockList.UncommittedBlocks {
		amendList = append(amendList, storage.Block{v.Name, storage.BlockStatusUncommitted})
	}
	err = f.client.PutBlockList(cnt, blob, amendList)
	if err != nil {
		return 0, err
	}
	return 0, nil
}

func (f *AzureFile) Close() error {
	return nil
}

// AzureClient -> Glob function
// only supports '*', '?'
// Syntax:
// cntName?/part.*
func (c *AzureClient) Glob(pattern string) (matches []string, err error) {
	afterSplit := strings.Split(pattern, "/")
	cntPattern, blobPattern := afterSplit[0], afterSplit[1]
	if len(afterSplit) != 2 {
		return nil, fmt.Errorf("Glob pattern should follow the Syntax")
	}
	resp, err := c.blobClient.ListContainers(storage.ListContainersParameters{Prefix: ""})
	if err != nil {
		return nil, err
	}
	for _, cnt := range resp.Containers {
		matched, err := path.Match(cntPattern, cnt.Name)
		if err != nil {
			return nil, err
		}
		if !matched {
			continue
		}
		resp, err := c.blobClient.ListBlobs(cnt.Name, storage.ListBlobsParameters{Marker: ""})
		if err != nil {
			return nil, err
		}
		for _, v := range resp.Blobs {
			matched, err := path.Match(blobPattern, v.Name)
			if err != nil {
				return nil, err
			}
			if matched {
				matches = append(matches, cnt.Name+"/"+v.Name)
			}
		}
	}
	return matches, nil
}

// NewAzureClient function
// NewClient constructs a StorageClient and blobStorageClinet.
// This should be used if the caller wants to specify
// whether to use HTTPS, a specific REST API version or a
// custom storage endpoint than Azure Public Cloud.
// Recommended API version "2014-02-14"
// synax :
// AzurestorageAccountName, AzurestorageAccountKey, "core.chinacloudapi.cn", "2014-02-14", true
func NewAzureClient(accountName, accountKey, blobServiceBaseUrl, apiVersion string, useHttps bool) (*AzureClient, error) {
	cli, err := storage.NewClient(accountName, accountKey, blobServiceBaseUrl, apiVersion, useHttps)
	if err != nil {
		return nil, err
	}
	return &AzureClient{
		client:     &cli,
		blobClient: cli.GetBlobService(),
	}, nil
}
