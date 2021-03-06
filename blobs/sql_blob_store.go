package blobs

import (
	"bytes"
	"database/sql"
	"io"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/opentracing/opentracing-go"
)

type sqlBlobStore struct {
	snapshotInterval int
	db               *sqlx.DB
}

// NewSQLBlobStore creates a new blob store on the given DB , the DB should already have tables in place
func NewSQLBlobStore(db *sqlx.DB) (Store, error) {
	return &sqlBlobStore{
		db: db,
	}, nil
}

// Create implements BlobStore - this buffers the blob to send to the DB
func (s *sqlBlobStore) Create(prefix string, contentType string, input io.Reader) (*Blob, error) {
	id, err := uuid.NewRandom()

	if err != nil {
		log.WithField("content_type", contentType).WithError(err).Errorf("Error generating blob ID")
		return nil, err
	}

	buf := bytes.Buffer{}
	_, err = buf.ReadFrom(input)

	if err != nil {
		return nil, err
	}
	data := buf.Bytes()

	idString := id.String()

	span := opentracing.StartSpan("sql_create_blob")
	defer span.Finish()
	_, err = s.db.Exec("INSERT INTO blobs(prefix,blob_id,blob_data) VALUES(?,?,?)", prefix, idString, data)
	if err != nil {
		log.WithField("content_type", contentType).WithField("blob_length", len(data)).WithError(err).Errorf("Error inserting blob into db")
		return nil, err
	}

	return &Blob{
		ID:          idString,
		Length:      int64(len(data)),
		ContentType: contentType,
	}, nil
}

// Read implements BlobStore - this buffers the blob before creating a reader
func (s *sqlBlobStore) Read(flowID string, blobID string) (io.Reader, error) {
	span := opentracing.StartSpan("sql_read_blob")
	defer span.Finish()
	row := s.db.QueryRowx("SELECT blob_data FROM blobs where blob_id = ?", blobID)
	if row.Err() != nil {
		log.WithField("blob_id", blobID).WithError(row.Err()).Errorf("Error querying blob from DB ")
		return nil, row.Err()
	}

	var blobData []byte
	err := row.Scan(&blobData)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrBlobNotFound
		}
		log.WithField("blob_id", blobID).WithError(row.Err()).Errorf("Error reading blob from DB")
		return nil, err
	}
	
	log.WithField("flow_id", flowID).WithField("blob_id", blobID).WithField("length", len(blobData)).Debugf("Successfully read blob from DB")
	return bytes.NewBuffer(blobData), nil

}
