package appliance

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Legacy array responses remain available to clients that do not request pagination.
func pagedList[T any](c *gin.Context, db *gorm.DB, searchColumns []string, legacyLimit int) {
	db = db.WithContext(c.Request.Context())
	rows := []T{}
	if c.Query("page") == "" && c.Query("page_size") == "" && c.Query("q") == "" {
		if legacyLimit > 0 {
			db = db.Limit(legacyLimit)
		}
		if err := db.Find(&rows).Error; err != nil {
			failure(c, 500, err)
			return
		}
		c.JSON(200, rows)
		return
	}
	page, e1 := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, e2 := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	q := strings.TrimSpace(c.Query("q"))
	if e1 != nil || e2 != nil || page < 1 || page > 100000000 || size < 1 || size > 100 || len(q) > 200 {
		failure(c, 400, errors.New("分页参数无效：页码从 1 开始，每页 1 到 100 条，关键词不超过 200 字节"))
		return
	}
	if q != "" {
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
		clauses := []string{}
		args := []any{}
		for _, col := range searchColumns {
			clauses = append(clauses, col+` LIKE ? ESCAPE '\'`)
			args = append(args, "%"+escaped+"%")
		}
		db = db.Where("("+strings.Join(clauses, " OR ")+")", args...)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		failure(c, 500, err)
		return
	}
	if err := db.Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		failure(c, 500, err)
		return
	}
	c.JSON(200, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
}
