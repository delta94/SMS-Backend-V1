// add file in v.1.0.5
// request_xlsx.go is file that definition request entity about xlsx handling API

package entity

import "mime/multipart"

type AddUnsignedStudentsFromExcelRequest struct {
	Excel *multipart.FileHeader `form:"excel" validate:"required"`
}
