package main

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

type Group struct {
	ID        uint      `gorm:"primarykey" json:"id" form:"id"`
	Name      string    `json:"name" form:"name"`
	ParentID  *uint     `json:"parent_id" form:"parent_id"`
	Config    *string   `json:"config" form:"config"`
	Users     []User    `gorm:"foreignKey:Gid;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	CreatedAt time.Time `json:"createdAt" form:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt" form:"updatedAt"`
}

func (g *Group) BeforeCreate(tx *gorm.DB) (err error) {
	if g.ParentID == nil && g.Name != "Default" {
		return errors.New("必须指定父节点")
	}

	if g.ParentID != nil {
		var parent Group
		if err := tx.First(&parent, *g.ParentID).Error; err != nil {
			return errors.New("父节点不存在")
		}
	}

	return nil
}

func (g *Group) Create() error {
	result := db.Create(&g)
	return result.Error
}

func (g *Group) Get(id string) Group {
	result := db.First(&g, id)
	if result.Error != nil {
		logger.Error(context.Background(), result.Error.Error())
		return Group{}
	}

	return *g
}

func (g *Group) All() []Group {
	var groups []Group

	result := db.Model(&Group{}).WithContext(context.Background()).Find(&groups)
	if result.Error != nil {
		logger.Error(context.Background(), result.Error.Error())
		return []Group{}
	}

	return groups
}

func (g *Group) Update() error {
	result := db.Model(&g).Updates(&g)
	return result.Error
}

func (g *Group) Delete(id string) error {
	// 取出待删分组，禁止删除 Default 根分组
	var target Group
	if err := db.First(&target, id).Error; err != nil {
		return err
	}
	if target.Name == "Default" {
		return errors.New("Default 根分组不可删除")
	}
	// 找到 Default 根分组，作为成员与子分组的归处
	var def Group
	if err := db.Where("name = ?", "Default").First(&def).Error; err != nil {
		return errors.New("未找到 Default 根分组，无法安全删除")
	}
	// 把直接成员改挂 Default（不再级联删除用户）
	if err := db.Model(&User{}).Where("gid = ?", target.ID).Update("gid", def.ID).Error; err != nil {
		return err
	}
	// 把直接子分组改挂 Default，避免产生孤儿分组
	if err := db.Model(&Group{}).Where("parent_id = ?", target.ID).Update("parent_id", def.ID).Error; err != nil {
		return err
	}
	// 此时该分组已无直接成员与子分组依赖，可安全硬删除
	result := db.Unscoped().Delete(&Group{}, id)
	return result.Error
}

func (g *Group) GetUsers(id string) []User {
	var users []User

	result := db.WithContext(context.Background()).
		Where(`
			gid IN (
				WITH RECURSIVE group_tree AS (
					SELECT id, parent_id
					FROM "group"
					WHERE id = ?
		
					UNION ALL
		
					SELECT g.id, g.parent_id
					FROM "group" g
					JOIN group_tree gt ON g.parent_id = gt.id
				)
				SELECT id FROM group_tree
			)
		`, id).
		Find(&users)

	if result.Error != nil {
		logger.Error(context.Background(), result.Error.Error())
		return []User{}
	}

	return users
}

func (Group) TableName() string {
	return "group"
}
