package main

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"strconv"

	"github.com/disintegration/imaging"
	"github.com/gofrs/uuid"
	"github.com/knadh/listmonk/internal/media"
	"github.com/labstack/echo"
)

const (
	thumbPrefix   = "thumb_"
	thumbnailSize = 90
)

// imageMimes is the list of image types allowed to be uploaded.
var imageMimes = []string{
	"image/jpg",
	"image/jpeg",
	"image/png",
	"image/svg",
	"image/gif"}

// handleUploadMedia handles media file uploads.
func handleUploadMedia(c echo.Context) error {
	var (
		app     = c.Get("app").(*App)
		cleanUp = false
	)
	file, err := c.FormFile("file")
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest,
			fmt.Sprintf("Invalid file uploaded: %v", err))
	}

	// Validate MIME type with the list of allowed types.
	var typ = file.Header.Get("Content-type")
	if ok := validateMIME(typ, imageMimes); !ok {
		return echo.NewHTTPError(http.StatusBadRequest,
			fmt.Sprintf("Unsupported file type (%s) uploaded.", typ))
	}

	// Generate filename
	fName := generateFileName(file.Filename)

	// Read file contents in memory
	src, err := file.Open()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest,
			fmt.Sprintf("Error reading file: %s", err))
	}
	defer src.Close()

	// Upload the file.
	fName, err = app.media.Put(fName, typ, src)
	if err != nil {
		app.log.Printf("error uploading file: %v", err)
		cleanUp = true
		return echo.NewHTTPError(http.StatusInternalServerError,
			fmt.Sprintf("Error uploading file: %s", err))
	}

	defer func() {
		// If any of the subroutines in this function fail,
		// the uploaded image should be removed.
		if cleanUp {
			app.media.Delete(fName)
			app.media.Delete(thumbPrefix + fName)
		}
	}()

	// Create thumbnail from file.
	thumbFile, err := createThumbnail(file)
	if err != nil {
		cleanUp = true
		app.log.Printf("error resizing image: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			fmt.Sprintf("Error resizing image: %s", err))
	}

	// Upload thumbnail.
	thumbfName, err := app.media.Put(thumbPrefix+fName, typ, thumbFile)
	if err != nil {
		cleanUp = true
		app.log.Printf("error saving thumbnail: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			fmt.Sprintf("Error saving thumbnail: %s", err))
	}

	uu, err := uuid.NewV4()
	if err != nil {
		app.log.Printf("error generating UUID: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "Error generating UUID")
	}

	// Write to the DB.
	if _, err := app.queries.InsertMedia.Exec(uu, fName, thumbfName, app.constants.MediaProvider); err != nil {
		cleanUp = true
		app.log.Printf("error inserting uploaded file to db: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			fmt.Sprintf("Error saving uploaded file to db: %s", pqErrMsg(err)))
	}
	return c.JSON(http.StatusOK, okResp{true})
}

func handleGetMedium(c echo.Context) error {
	var (
		app   = c.Get("app").(*App)
		out   media.Media
		id, _ = strconv.Atoi(c.Param("id"))
	)

	if id < 1 {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid ID.")
	}

	if err := app.queries.GetMedium.QueryRowx(app.constants.MediaProvider, id).StructScan(&out); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError,
			fmt.Sprintf("Error fetching media list: %s", pqErrMsg(err)))
	}

	out.URL = app.media.Get(out.Filename)
	out.File = app.media.GetData(out.Filename)
	out.ThumbURL = app.media.Get(thumbPrefix + out.Filename)
	out.Supports = app.media.Supports()

	return c.JSON(http.StatusOK, okResp{out})
}

// handleGetMedia handles retrieval of uploaded media.
func handleGetMedia(c echo.Context) error {
	var (
		app = c.Get("app").(*App)
		out = []media.Media{}
	)

	if err := app.queries.GetMedia.Select(&out, app.constants.MediaProvider); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError,
			fmt.Sprintf("Error fetching media list: %s", pqErrMsg(err)))
	}

	for i := 0; i < len(out); i++ {
		out[i].File = app.media.GetData(out[i].Filename)
		out[i].ThumbURL = app.media.Get(thumbPrefix + out[i].Filename)
		out[i].URL = app.media.Get(out[i].Filename)
		out[i].Supports = app.media.Supports()
	}

	return c.JSON(http.StatusOK, okResp{out})
}

// deleteMedia handles deletion of uploaded media.
func handleDeleteMedia(c echo.Context) error {
	var (
		app   = c.Get("app").(*App)
		id, _ = strconv.Atoi(c.Param("id"))
	)

	if id < 1 {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid ID.")
	}

	var m media.Media
	if err := app.queries.DeleteMedia.Get(&m, id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError,
			fmt.Sprintf("Error deleting media: %s", pqErrMsg(err)))
	}

	app.media.Delete(m.Filename)
	app.media.Delete(thumbPrefix + m.Filename)
	return c.JSON(http.StatusOK, okResp{true})
}

// createThumbnail reads the file object and returns a smaller image
func createThumbnail(file *multipart.FileHeader) (*bytes.Reader, error) {
	src, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer src.Close()

	img, err := imaging.Decode(src)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError,
			fmt.Sprintf("Error decoding image: %v", err))
	}

	// Encode the image into a byte slice as PNG.
	var (
		thumb = imaging.Resize(img, thumbnailSize, 0, imaging.Lanczos)
		out   bytes.Buffer
	)
	if err := imaging.Encode(&out, thumb, imaging.PNG); err != nil {
		return nil, err
	}
	return bytes.NewReader(out.Bytes()), nil
}
