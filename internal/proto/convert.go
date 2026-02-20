package proto

import (
	"github.com/viperadnan-git/opendebrid/internal/core/statusloop"
	pb "github.com/viperadnan-git/opendebrid/internal/proto/gen"
)

// ReportsToProto converts status reports to the proto wire type.
func ReportsToProto(reports []statusloop.StatusReport) []*pb.JobStatusReport {
	out := make([]*pb.JobStatusReport, len(reports))
	for i, r := range reports {
		out[i] = &pb.JobStatusReport{
			JobId:          r.JobID,
			EngineJobId:    r.EngineJobID,
			Status:         r.Status,
			Progress:       r.Progress,
			Speed:          r.Speed,
			TotalSize:      r.TotalSize,
			DownloadedSize: r.DownloadedSize,
			Name:           r.Name,
			Error:          r.Error,
		}
	}
	return out
}
