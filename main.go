package main

import (
	"gateway/consul"
	consulagent "gateway/consul/agent"
	"gateway/entity/validator"
	"gateway/handler"
	"gateway/middleware"
	announcementproto "gateway/proto/golang/announcement"
	authproto "gateway/proto/golang/auth"
	clubproto "gateway/proto/golang/club"
	outingproto "gateway/proto/golang/outing"
	scheduleproto "gateway/proto/golang/schedule"
	"gateway/tool/env"
	topic "gateway/utils/topic/golang"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/bshuster-repo/logrus-logstash-hook"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/hashicorp/consul/api"
	"github.com/micro/go-micro/v2/client"
	grpccli "github.com/micro/go-micro/v2/client/grpc"
	"github.com/micro/go-micro/v2/client/selector"
	"github.com/micro/go-micro/v2/transport/grpc"
	"github.com/sirupsen/logrus"
	"github.com/uber/jaeger-client-go"
	jaegercfg "github.com/uber/jaeger-client-go/config"
	"log"
	"net/http"
	"os"
	"time"
)

// start profiling in this package init function (add in v.1.0.2)
import _ "gateway/tool/profiling"

func main() {
	// create consul connection & consul agent
	consulCfg := api.DefaultConfig()
	consulCfg.Address = env.GetAndFatalIfNotExits("CONSUL_ADDRESS") // change how to get env from local in v.1.0.2
	consulCli, err := api.NewClient(consulCfg)
	if err != nil {
		log.Fatalf("unable to connect consul agent, err: %v", err)
	}
	consulAgent := consulagent.Default(
		consulagent.Strategy(selector.RoundRobin),
		consulagent.Client(consulCli),
		consulagent.Services([]consul.ServiceName{topic.AuthServiceName, topic.ClubServiceName,  // add in v.1.0.2
			topic.OutingServiceName, topic.ScheduleServiceName, topic.AnnouncementServiceName}),
	)

	// create jaeger connection
	jaegerAddr := env.GetAndFatalIfNotExits("JAEGER_ADDRESS")
	apiTracer, closer, err := jaegercfg.Configuration{
		ServiceName: "DMS.SMS.v1.api.gateway", // add const in topic
		Reporter: &jaegercfg.ReporterConfig{LogSpans: true, LocalAgentHostPort: jaegerAddr},
		Sampler: &jaegercfg.SamplerConfig{Type: jaeger.SamplerTypeConst, Param: 1},
	}.NewTracer()
	if err != nil {
		log.Fatalf("error while creating new tracer for service, err: %v", err)
	}
	defer func() {
		_ = closer.Close()
	}()

	// create aws session (add in v.1.0.2)
	awsId := env.GetAndFatalIfNotExits("SMS_AWS_ID")
	awsKey := env.GetAndFatalIfNotExits("SMS_AWS_KEY")
	s3Region := env.GetAndFatalIfNotExits("SMS_AWS_REGION")
	awsSession, err := session.NewSession(&aws.Config{
		Region:      aws.String(s3Region),
		Credentials: credentials.NewStaticCredentials(awsId, awsKey, ""),
	})

	// gRPC service client
	gRPCCli := grpccli.NewClient(client.Transport(grpc.NewTransport()))
	authSrvCli := struct {
		authproto.AuthAdminService
		authproto.AuthStudentService
		authproto.AuthTeacherService
		authproto.AuthParentService
	}{
		AuthAdminService:   authproto.NewAuthAdminService(topic.AuthServiceName, gRPCCli),
		AuthStudentService: authproto.NewAuthStudentService(topic.AuthServiceName, gRPCCli),
		AuthTeacherService: authproto.NewAuthTeacherService(topic.AuthServiceName, gRPCCli),
		AuthParentService:  authproto.NewAuthParentService(topic.AuthServiceName, gRPCCli),
	}
	clubSrvCli := struct {
		clubproto.ClubAdminService
		clubproto.ClubStudentService
		clubproto.ClubLeaderService
	}{
		ClubAdminService:   clubproto.NewClubAdminService(topic.ClubServiceName, gRPCCli),
		ClubStudentService: clubproto.NewClubStudentService(topic.ClubServiceName, gRPCCli),
		ClubLeaderService:  clubproto.NewClubLeaderService(topic.ClubServiceName, gRPCCli),
	}
	outingSrvCli := struct {
		outingproto.OutingStudentService
		outingproto.OutingTeacherService
		outingproto.OutingParentsService
	} {
		OutingStudentService: outingproto.NewOutingStudentService("", gRPCCli),
		OutingTeacherService: outingproto.NewOutingTeacherService("", gRPCCli),
		OutingParentsService: outingproto.NewOutingParentsService("", gRPCCli),
	}
	scheduleSrvCli := struct {
		scheduleproto.ScheduleService
	} {
		ScheduleService: scheduleproto.NewScheduleService("schedule", gRPCCli),
	}
	announcementSrvCli := struct {
		announcementproto.AnnouncementService
	} {
		AnnouncementService: announcementproto.NewAnnouncementService("announcement", gRPCCli),
	}

	// create http request handler
	httpHandler := handler.Default(
		handler.ConsulAgent(consulAgent),
		handler.Validate(validator.New()),
		handler.Tracer(apiTracer),
		handler.AWSSession(awsSession),
		handler.Location(time.UTC),
		handler.AuthService(authSrvCli),
		handler.ClubService(clubSrvCli),
		handler.OutingService(outingSrvCli),
		handler.ScheduleService(scheduleSrvCli),
		handler.AnnouncementService(announcementSrvCli),
	)

	// create log file
	if _, err := os.Stat("/usr/share/filebeat/log/dms-sms"); os.IsNotExist(err) {
		if err = os.MkdirAll("/usr/share/filebeat/log/dms-sms", os.ModePerm); err != nil { log.Fatal(err) }
	}
	authLog, err := os.OpenFile("/usr/share/filebeat/log/dms-sms/auth.log", os.O_CREATE|os.O_RDWR|os.O_APPEND, 0755)
	if err != nil { log.Fatal(err) }
	clubLog, err := os.OpenFile("/usr/share/filebeat/log/dms-sms/club.log", os.O_CREATE|os.O_RDWR|os.O_APPEND, 0755)
	if err != nil { log.Fatal(err) }
	outingLog, err := os.OpenFile("/usr/share/filebeat/log/dms-sms/outing.log", os.O_CREATE|os.O_RDWR|os.O_APPEND, 0755)
	if err != nil { log.Fatal(err) }
	scheduleLog, err := os.OpenFile("/usr/share/filebeat/log/dms-sms/schedule.log", os.O_CREATE|os.O_RDWR|os.O_APPEND, 0755)
	if err != nil { log.Fatal(err) }
	announcementLog, err := os.OpenFile("/usr/share/filebeat/log/dms-sms/announcement.log", os.O_CREATE|os.O_RDWR|os.O_APPEND, 0755)
	if err != nil { log.Fatal(err) }
	openApiLog, err := os.OpenFile("/usr/share/filebeat/log/dms-sms/open-api.log", os.O_CREATE|os.O_RDWR|os.O_APPEND, 0755)
	if err != nil { log.Fatal(err) }

	// create logger & add hooks
	authLogger := logrus.New()
	authLogger.Hooks.Add(logrustash.New(authLog, logrustash.DefaultFormatter(logrus.Fields{"service": "auth"})))
	clubLogger := logrus.New()
	clubLogger.Hooks.Add(logrustash.New(clubLog, logrustash.DefaultFormatter(logrus.Fields{"service": "club"})))
	outingLogger := logrus.New()
	outingLogger.Hooks.Add(logrustash.New(outingLog, logrustash.DefaultFormatter(logrus.Fields{"service": "outing"})))
	scheduleLogger := logrus.New()
	scheduleLogger.Hooks.Add(logrustash.New(scheduleLog, logrustash.DefaultFormatter(logrus.Fields{"service": "schedule"})))
	announcementLogger := logrus.New()
	announcementLogger.Hooks.Add(logrustash.New(announcementLog, logrustash.DefaultFormatter(logrus.Fields{"service": "announcement"})))
	openApiLogger := logrus.New()
	openApiLogger.Hooks.Add(logrustash.New(openApiLog, logrustash.DefaultFormatter(logrus.Fields{"service": "open-api"})))

	// create router
	gin.SetMode(gin.ReleaseMode)
	router := gin.Default()

	// routing ping & pong API
	healthCheckRouter := router.Group("/")
	healthCheckRouter.GET("/ping", func(c *gin.Context) { // add in v.1.0.2
		c.JSON(http.StatusOK, "pong")
	})

	// routing API to use in consul watch
	consulWatchRouter := router.Group("/")
	consulWatchRouter.POST("/events/types/consul-change", httpHandler.PublishConsulChangeEvent) // add in v.1.0.2

	// add middleware handler
	corsConfig := cors.DefaultConfig()
	corsConfig.AllowAllOrigins = true
	corsConfig.AllowHeaders = append(corsConfig.AllowHeaders, "Authorization", "authorization", "Request-Security")
	corsHandler := cors.New(corsConfig)
	router.Use(corsHandler, middleware.SecurityFilter(), middleware.Correlator()) // middleware.DosDetector() 삭제

	// routing auth service API
	authRouter := router.Group("/", middleware.LogEntrySetter(authLogger))
	// auth service api for admin
	authRouter.POST("/v1/students", httpHandler.CreateNewStudent)
	authRouter.POST("/v1/teachers", httpHandler.CreateNewTeacher)
	authRouter.POST("/v1/parents", httpHandler.CreateNewParent)
	authRouter.POST("/v1/login/admin", httpHandler.LoginAdminAuth)
	// auth service api for student
	authRouter.POST("/v1/login/student", httpHandler.LoginStudentAuth)
	authRouter.PUT("/v1/students/uuid/:student_uuid/password", httpHandler.ChangeStudentPW)
	authRouter.GET("/v1/students/uuid/:student_uuid", httpHandler.GetStudentInformWithUUID)
	authRouter.GET("/v1/student-uuids", httpHandler.GetStudentUUIDsWithInform)
	authRouter.POST("/v1/students/with-uuids", httpHandler.GetStudentInformsWithUUIDs)
	authRouter.GET("/v1/students/uuid/:student_uuid/parent", httpHandler.GetParentWithStudentUUID)
	// auth service api for teacher
	authRouter.POST("/v1/login/teacher", httpHandler.LoginTeacherAuth)
	authRouter.PUT("/v1/teachers/uuid/:teacher_uuid/password", httpHandler.ChangeTeacherPW)
	authRouter.GET("/v1/teachers/uuid/:teacher_uuid", httpHandler.GetTeacherInformWithUUID)
	authRouter.GET("/v1/teacher-uuids", httpHandler.GetTeacherUUIDsWithInform)
	// auth service api for parent
	authRouter.POST("/v1/login/parent", httpHandler.LoginParentAuth)
	authRouter.PUT("/v1/parents/uuid/:parent_uuid/password", httpHandler.ChangeParentPW)
	authRouter.GET("/v1/parents/uuid/:parent_uuid", httpHandler.GetParentInformWithUUID)
	authRouter.GET("/v1/parent-uuids", httpHandler.GetParentUUIDsWithInform)
	authRouter.GET("/v1/parents/uuid/:parent_uuid/children", httpHandler.GetChildrenInformsWithUUID)

	// routing club service API
	clubRouter := router.Group("/", middleware.LogEntrySetter(clubLogger))
	// club service api for admin
	clubRouter.POST("/v1/clubs", httpHandler.CreateNewClub)
	// club service api for student
	clubRouter.GET("/v1/clubs/sorted-by/update-time", httpHandler.GetClubsSortByUpdateTime)
	clubRouter.GET("/v1/recruitments/sorted-by/create-time", httpHandler.GetRecruitmentsSortByCreateTime)
	clubRouter.GET("/v1/clubs/uuid/:club_uuid", httpHandler.GetClubInformWithUUID)
	clubRouter.GET("/v1/clubs", httpHandler.GetClubInformsWithUUIDs)
	clubRouter.GET("/v1/recruitments/uuid/:recruitment_uuid", httpHandler.GetRecruitmentInformWithUUID)
	clubRouter.GET("/v1/clubs/uuid/:club_uuid/recruitment-uuid", httpHandler.GetRecruitmentUUIDWithClubUUID)
	clubRouter.GET("/v1/recruitment-uuids", httpHandler.GetRecruitmentUUIDsWithClubUUIDs)
	clubRouter.GET("/v1/clubs/property/fields", httpHandler.GetAllClubFields)
	clubRouter.GET("/v1/clubs/count", httpHandler.GetTotalCountOfClubs)
	clubRouter.GET("/v1/recruitments/count", httpHandler.GetTotalCountOfCurrentRecruitments)
	clubRouter.GET("/v1/leaders/uuid/:leader_uuid/club-uuid", httpHandler.GetClubUUIDWithLeaderUUID)
	// club service api for club leader
	clubRouter.DELETE("/v1/clubs/uuid/:club_uuid", httpHandler.DeleteClubWithUUID)
	clubRouter.POST("/v1/clubs/uuid/:club_uuid/members", httpHandler.AddClubMember)
	clubRouter.DELETE("/v1/clubs/uuid/:club_uuid/members/:student_uuid", httpHandler.DeleteClubMember)
	clubRouter.PUT("/v1/clubs/uuid/:club_uuid/leader", httpHandler.ChangeClubLeader)
	clubRouter.PATCH("/v1/clubs/uuid/:club_uuid", httpHandler.ModifyClubInform)
	clubRouter.POST("/v1/recruitments", httpHandler.RegisterRecruitment)
	clubRouter.PATCH("/v1/recruitments/uuid/:recruitment_uuid", httpHandler.ModifyRecruitment)
	clubRouter.DELETE("/v1/recruitments/uuid/:recruitment_uuid", httpHandler.DeleteRecruitment)

	// routing outing service API
	outingRouter := router.Group("/", middleware.LogEntrySetter(outingLogger))
	outingRouter.POST("/v1/outings", httpHandler.CreateOuting)
	outingRouter.GET("/v1/students/uuid/:student_uuid/outings", httpHandler.GetStudentOutings)
	outingRouter.GET("/v1/outings/uuid/:outing_uuid", httpHandler.GetOutingInform)
	outingRouter.GET("/v1/outings/uuid/:outing_uuid/card", httpHandler.GetCardAboutOuting)
	outingRouter.POST("/v1/outings/uuid/:outing_uuid/actions/:action", httpHandler.TakeActionInOuting)
	outingRouter.GET("/v1/outings/with-filter", httpHandler.GetOutingWithFilter)
	outingRouter.GET("/v1/outings/code/:OCode", httpHandler.GetOutingByOCode)

	// routing schedule service API
	scheduleRouter := router.Group("/", middleware.LogEntrySetter(scheduleLogger))
	scheduleRouter.POST("/v1/schedules", httpHandler.CreateSchedule)
	scheduleRouter.GET("/v1/schedules/years/:year/months/:month", httpHandler.GetSchedule)
	scheduleRouter.GET("/v1/time-tables/years/:year/months/:month/days/:day", httpHandler.GetTimeTable)
	scheduleRouter.PATCH("/v1/schedules/uuid/:schedule_uuid", httpHandler.UpdateSchedule)
	scheduleRouter.DELETE("/v1/schedules/uuid/:schedule_uuid", httpHandler.DeleteSchedule)

	// routing announcement service API
	announcementRouter := router.Group("/", middleware.LogEntrySetter(announcementLogger))
	announcementRouter.POST("/v1/announcements", httpHandler.CreateAnnouncement)
	announcementRouter.GET("/v1/announcements/types/:type", httpHandler.GetAnnouncements)
	announcementRouter.GET("/v1/announcements/uuid/:announcement_uuid", httpHandler.GetAnnouncementDetail)
	announcementRouter.PATCH("/v1/announcements/uuid/:announcement_uuid", httpHandler.UpdateAnnouncement)
	announcementRouter.DELETE("/v1/announcements/uuid/:announcement_uuid", httpHandler.DeleteAnnouncement)
	announcementRouter.GET("/v1/students/uuid/:student_uuid/announcement-check", httpHandler.CheckAnnouncement)
	announcementRouter.GET("/v1/announcements/types/:type/query/:search_query", httpHandler.SearchAnnouncements)
	announcementRouter.GET("/v1/announcements/writer-uuid/:writer_uuid", httpHandler.GetMyAnnouncements)

	// routing open-api agent API
	openApiRouter := router.Group("/", middleware.LogEntrySetter(openApiLogger))
	openApiRouter.GET("/naver-open-api/search/local", httpHandler.GetPlaceWithNaverOpenAPI)

	// run server
	log.Fatal(router.Run(":80"))
}
